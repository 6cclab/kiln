package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/execenv"
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
				if err := env.WriteFile(absolutePath, []byte(in.Content)); err != nil {
					return tool.Errorf("%s", err), nil
				}
				if ctx.Err() != nil {
					return tool.Errorf("Operation aborted"), nil
				}
				return tool.Text(fmt.Sprintf("Successfully wrote to %s", in.Path)), nil
			})
		},
	}
}
