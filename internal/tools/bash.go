package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

var bashParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"command": {"type": "string", "description": "Bash command to execute"},
		"timeout": {"type": "number", "description": "Timeout in seconds (optional, no default timeout)"}
	},
	"required": ["command"]
}`)

var bashDescription = fmt.Sprintf(
	"Execute a bash command in the current working directory. Returns combined stdout and stderr. "+
		"Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. "+
		"Optionally provide a timeout in seconds.",
	execenv.DefaultMaxLines, execenv.DefaultMaxBytes/1024,
)

type bashArgs struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout"`
}

// bashDetails is the machine-readable payload alongside a bash result,
// mirroring bash.js's BashToolDetails. Only populated when the output was
// truncated.
type bashDetails struct {
	FullOutputPath string             `json:"fullOutputPath,omitempty"`
	Truncation     *truncationDetails `json:"truncation,omitempty"`
}

// truncationDetails is execenv.TruncationResult minus Content, mirroring
// pi's ShellOutputTruncation (Omit<TruncationResult, "content">).
type truncationDetails struct {
	FirstLineExceedsLimit bool                `json:"firstLineExceedsLimit"`
	LastLinePartial       bool                `json:"lastLinePartial"`
	MaxBytes              int                 `json:"maxBytes"`
	MaxLines              int                 `json:"maxLines"`
	OutputBytes           int                 `json:"outputBytes"`
	OutputLines           int                 `json:"outputLines"`
	TotalBytes            int                 `json:"totalBytes"`
	TotalLines            int                 `json:"totalLines"`
	Truncated             bool                `json:"truncated"`
	TruncatedBy           execenv.TruncatedBy `json:"truncatedBy"`
}

func toTruncationDetails(t execenv.TruncationResult) *truncationDetails {
	return &truncationDetails{
		FirstLineExceedsLimit: t.FirstLineExceedsLimit,
		LastLinePartial:       t.LastLinePartial,
		MaxBytes:              t.MaxBytes,
		MaxLines:              t.MaxLines,
		OutputBytes:           t.OutputBytes,
		OutputLines:           t.OutputLines,
		TotalBytes:            t.TotalBytes,
		TotalLines:            t.TotalLines,
		Truncated:             t.Truncated,
		TruncatedBy:           t.TruncatedBy,
	}
}

// BashTool builds the bash built-in, mirroring bash.js's createBashTool:
// runs command through env.Exec, streams the bounded combined
// stdout+stderr view through onUpdate as it grows, and on completion
// appends pi's exact "[Showing lines X-Y of Z ...]" truncation footer
// before returning the result. A non-zero exit code or a timeout is
// reported as an IsError result carrying the same output text, mirroring
// bash.js throwing with the output prepended to the failure message.
func BashTool(env *execenv.Env) *tool.Tool {
	return &tool.Tool{
		Name:        "bash",
		Label:       "bash",
		Description: bashDescription,
		Parameters:  bashParameters,
		Execute: func(ctx context.Context, args json.RawMessage, onUpdate tool.Update, _ tool.Invocation) (tool.Result, error) {
			var in bashArgs
			if err := json.Unmarshal(args, &in); err != nil {
				return tool.Errorf("invalid arguments: %s", err), nil
			}
			var timeout time.Duration
			if in.Timeout != nil {
				if *in.Timeout <= 0 {
					return tool.Errorf("Invalid timeout: must be a finite number of seconds"), nil
				}
				timeout = time.Duration(*in.Timeout * float64(time.Second))
			}

			onUpdate(tool.Result{})

			var view execenv.ShellOutputView
			var haveView bool
			result, execErr := env.Exec(ctx, in.Command, execenv.ExecOptions{
				Timeout:    timeout,
				InheritEnv: true,
				Capture: execenv.CaptureLimits{
					MaxBytes: execenv.DefaultMaxBytes,
					MaxLines: execenv.DefaultMaxLines,
					Retain:   execenv.RetainTail,
				},
				Spill: true,
				OnUpdate: func(update execenv.ShellOutputUpdate) {
					var current *execenv.ShellOutputView
					if haveView {
						current = &view
					}
					view = execenv.ApplyShellOutputUpdate(current, update)
					haveView = true
					onUpdate(tool.Result{Content: msg.Blocks{msg.Text(view.Text)}})
				},
			})

			outputText := view.Text
			truncation := view.Truncation
			spillPath := view.SpillPath
			if execErr == nil {
				outputText = result.Text
				truncation = result.Truncation
				spillPath = result.SpillPath
			}

			var details *bashDetails
			if truncation.Truncated {
				details = &bashDetails{Truncation: toTruncationDetails(truncation), FullOutputPath: spillPath}
				startLine := truncation.TotalLines - truncation.OutputLines + 1
				endLine := truncation.TotalLines
				switch {
				case truncation.LastLinePartial:
					lastLineSize := execenv.FormatSize(truncation.OutputBytes)
					outputText += fmt.Sprintf("\n\n[Showing last %s of line %d (line is %s). Full output: %s]",
						execenv.FormatSize(truncation.OutputBytes), endLine, lastLineSize, spillPath)
				case truncation.TruncatedBy == execenv.TruncatedByLines:
					outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]",
						startLine, endLine, truncation.TotalLines, spillPath)
				default:
					outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Full output: %s]",
						startLine, endLine, truncation.TotalLines, execenv.FormatSize(execenv.DefaultMaxBytes), spillPath)
				}
			}

			detailsJSON, _ := json.Marshal(details)
			if details == nil {
				detailsJSON = nil
			}

			if execErr != nil {
				status := execErr.Error()
				if result.TimedOut {
					status = fmt.Sprintf("Command timed out after %v seconds", timeout.Seconds())
				} else if ctx.Err() != nil {
					status = "Command aborted"
				}
				text := status
				if outputText != "" {
					text = outputText + "\n\n" + status
				}
				return tool.Result{Content: msg.Blocks{msg.Text(text)}, Details: detailsJSON, IsError: true}, nil
			}
			if result.ExitCode != 0 {
				text := fmt.Sprintf("Command exited with code %d", result.ExitCode)
				if outputText != "" {
					text = outputText + "\n\n" + text
				}
				return tool.Result{Content: msg.Blocks{msg.Text(text)}, Details: detailsJSON, IsError: true}, nil
			}
			if outputText == "" {
				outputText = "(no output)"
			}
			return tool.Result{Content: msg.Blocks{msg.Text(outputText)}, Details: detailsJSON}, nil
		},
	}
}
