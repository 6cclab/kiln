package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

var bashParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"command": {"type": "string", "description": "Bash command to execute"},
		"timeout": {"type": "number", "description": "Timeout in seconds (optional, default 120, max 600)"}
	},
	"required": ["command"]
}`)

var bashDescription = fmt.Sprintf(
	"Execute a bash command in the current working directory. Returns combined stdout and stderr. "+
		"Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. "+
		"Commands time out after 120 seconds unless a timeout (in seconds, up to 600) is given; "+
		"pass a longer one for slow builds or test suites. To leave a server running, background it "+
		"with its output redirected to a file (server >log 2>&1 &).",
	execenv.DefaultMaxLines, execenv.DefaultMaxBytes/1024,
)

// A command runs at most bashDefaultTimeout unless the model asks for
// longer, and never longer than bashMaxTimeout: with no bound, one
// command that never exits (a dev server started in the foreground)
// stalls the whole session.
const (
	bashDefaultTimeout = 120 * time.Second
	bashMaxTimeout     = 600 * time.Second
)

// bashTimeout resolves the model's timeout argument: the default when it
// gave none, capped at the maximum, and not ok when it is not a positive
// finite number of seconds.
func bashTimeout(seconds *float64) (time.Duration, bool) {
	if seconds == nil {
		return bashDefaultTimeout, true
	}
	s := *seconds
	if math.IsNaN(s) || s <= 0 {
		return 0, false
	}
	if s >= bashMaxTimeout.Seconds() {
		return bashMaxTimeout, true // also keeps a huge value from overflowing
	}
	return time.Duration(s * float64(time.Second)), true
}

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
			timeout, ok := bashTimeout(in.Timeout)
			if !ok {
				return tool.Errorf("Invalid timeout: must be a finite number of seconds"), nil
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
					status = fmt.Sprintf("Command timed out after %s", formatSeconds(timeout.Seconds()))
				} else if ctx.Err() != nil {
					status = "Command aborted"
				}
				text := appendStatus(outputText, status)
				return tool.Result{Content: msg.Blocks{msg.Text(text)}, Details: detailsJSON, IsError: true}, nil
			}
			if result.ExitCode != 0 {
				text := appendStatus(outputText, fmt.Sprintf("Command exited with code %d", result.ExitCode))
				return tool.Result{Content: msg.Blocks{msg.Text(text)}, Details: detailsJSON, IsError: true}, nil
			}
			if outputText == "" {
				outputText = "(no output)"
			}
			if result.JobsLeft {
				outputText += fmt.Sprintf("\n\n[A background job this command started is still running (process group %d). "+
					"It is stopped when the session ends; to stop it sooner, run: kill -- -%d. "+
					"Use bash_background for a process you need to check on.]", result.JobsGroup, result.JobsGroup)
			}
			return tool.Result{Content: msg.Blocks{msg.Text(outputText)}, Details: detailsJSON}, nil
		},
	}
}

// appendStatus joins a status line (a timeout or exit-code message) onto
// the command's output. outputText's own trailing newline(s) are trimmed
// first so the result has exactly one blank line between output and
// status, not two — two blank lines plus the output can exhaust a
// collapsed transcript block's line budget before the status line (the
// most important line on a failure) is ever reached.
func appendStatus(outputText, status string) string {
	outputText = strings.TrimRight(outputText, "\n")
	if outputText == "" {
		return status
	}
	return outputText + "\n\n" + status
}

// formatSeconds renders a duration in seconds with correct pluralization,
// e.g. "1 second" or "5 seconds".
func formatSeconds(seconds float64) string {
	if seconds == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%v seconds", seconds)
}
