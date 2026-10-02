package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

var writeParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Path to the file to write (relative or absolute)"},
		"content": {"type": "string", "description": "Content to write to the file"}
	},
	"required": ["path", "content"]
}`)

const writeDescription = "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories."

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// writeDetails is the machine-readable payload alongside a write result,
// mirroring editDetails (edit.go): a unified patch against whatever was on
// disk before (empty old content for a brand new file), plus NewFile so a
// renderer can show the "new file" tag the kiln diff block calls for
// instead of computing "did this path exist" a second time itself.
type writeDetails struct {
	NewFile bool   `json:"newFile"`
	Patch   string `json:"patch"`
}

// WriteTool builds the write built-in, mirroring write.js's
// createWriteTool: writes are serialized per path against concurrent
// edit/write calls (see withFileMutationLock), and parent directories are
// created automatically.
func WriteTool(env *execenv.Env) *tool.Tool {
	return &tool.Tool{
		Name:        "write",
		Label:       "write",
		Description: writeDescription,
		Parameters:  writeParameters,
		Execute: func(ctx context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var in writeArgs
			if err := json.Unmarshal(args, &in); err != nil {
				return tool.Errorf("invalid arguments: %s", err), nil
			}
			absolutePath := resolveToolPath(env, in.Path)
			return withFileMutationLock(absolutePath, func() (tool.Result, error) {
				if ctx.Err() != nil {
					return tool.Errorf("Operation aborted"), nil
				}
				if refusal, refused := refuseVolPath(absolutePath, in.Path); refused {
					return refusal, nil
				}
				if refusal, refused := refuseSymlink(env, absolutePath, in.Path); refused {
					return refusal, nil
				}
				// Read whatever is there before overwriting it, so the
				// result can report "new file" versus a real diff against
				// the previous content (kiln's diff block, transcript.go
				// RenderToolCall). A read error other than "does not
				// exist" is swallowed here rather than failing the write:
				// the write itself is still attempted, same as before this
				// field existed.
				existing, readErr := env.ReadFile(absolutePath)
				newFile := readErr != nil
				if err := env.WriteFile(absolutePath, []byte(in.Content)); err != nil {
					errText := err.Error()
					if hint := env.DidYouMeanHint(absolutePath); hint != "" {
						errText += " " + hint
					}
					return tool.Errorf("%s", errText), nil
				}
				if ctx.Err() != nil {
					return tool.Errorf("Operation aborted"), nil
				}
				details := writeDetails{NewFile: newFile}
				if newFile {
					details.Patch = generateUnifiedPatch(in.Path, "", in.Content)
				} else {
					details.Patch = generateUnifiedPatch(in.Path, string(existing), in.Content)
				}
				detailsJSON, _ := json.Marshal(details)
				return tool.Result{Content: msg.Blocks{msg.Text(fmt.Sprintf("Successfully wrote to %s", in.Path))}, Details: detailsJSON}, nil
			})
		},
	}
}
