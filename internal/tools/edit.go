package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/plural"
	"github.com/andrepato/harness/internal/tool"
)

var editParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Path to the file to edit (relative or absolute)"},
		"edits": {
			"type": "array",
			"description": "One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.",
			"items": {
				"type": "object",
				"properties": {
					"oldText": {"type": "string", "description": "Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},
					"newText": {"type": "string", "description": "Replacement text for this targeted edit."}
				},
				"required": ["oldText", "newText"]
			}
		}
	},
	"required": ["path", "edits"]
}`)

const editDescription = "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes."

type editOneArgs struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type editArgs struct {
	Path  string        `json:"path"`
	Edits []editOneArgs `json:"edits"`
}

// editDetails is the machine-readable payload alongside an edit result,
// mirroring edit.js's EditToolDetails.
type editDetails struct {
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine int    `json:"firstChangedLine,omitempty"`
}

// EditTool builds the edit built-in, mirroring edit.js's createEditTool:
// oldText is matched exactly first, then with fuzzy normalization
// (trailing whitespace, smart quotes, Unicode dashes, special spaces —
// see fuzzyFindText in editdiff.go); an ambiguous (non-unique) or missing
// match is an error, not a best-effort guess; a successful edit is
// serialized against concurrent write/edit calls on the same path and
// returns a unified diff plus a display-oriented diff string as Details.
func EditTool(env *execenv.Env) *tool.Tool {
	return &tool.Tool{
		Name:        "edit",
		Label:       "edit",
		Description: editDescription,
		Parameters:  editParameters,
		Execute: func(ctx context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var in editArgs
			if err := json.Unmarshal(args, &in); err != nil {
				return tool.Errorf("invalid arguments: %s", err), nil
			}
			if len(in.Edits) == 0 {
				return tool.Errorf("Edit tool input is invalid. edits must contain at least one replacement."), nil
			}
			absolutePath := resolveToolPath(env, in.Path)

			return withFileMutationLock(absolutePath, func() (tool.Result, error) {
				if ctx.Err() != nil {
					return tool.Errorf("Operation aborted"), nil
				}
				info, err := env.Stat(absolutePath)
				if err != nil {
					return tool.Errorf("Could not edit file: %s. Error code: %s.", in.Path, errCode(err)), nil
				}
				if info.Kind != execenv.KindFile && info.Kind != execenv.KindSymlink {
					return tool.Errorf("Could not edit file: %s. Path is not a file.", in.Path), nil
				}
				raw, err := env.ReadFile(absolutePath)
				if err != nil {
					return tool.Errorf("Could not edit file: %s. Error code: %s.", in.Path, errCode(err)), nil
				}
				if ctx.Err() != nil {
					return tool.Errorf("Operation aborted"), nil
				}

				bom, content := stripBOM(string(raw))
				originalEnding := detectLineEnding(content)
				normalizedContent := normalizeToLF(content)

				edits := make([]editInput, len(in.Edits))
				for i, e := range in.Edits {
					edits[i] = editInput(e)
				}
				applied, err := applyEditsToNormalizedContent(normalizedContent, edits, in.Path)
				if err != nil {
					return tool.Errorf("%s", err), nil
				}
				if ctx.Err() != nil {
					return tool.Errorf("Operation aborted"), nil
				}

				finalContent := bom + restoreLineEndings(applied.NewContent, originalEnding)
				if err := env.WriteFile(absolutePath, []byte(finalContent)); err != nil {
					return tool.Errorf("Could not edit file: %s. Error code: %s.", in.Path, errCode(err)), nil
				}
				if ctx.Err() != nil {
					return tool.Errorf("Operation aborted"), nil
				}

				diffResult := generateDiffString(applied.BaseContent, applied.NewContent)
				details := editDetails{
					Diff:  diffResult.Diff,
					Patch: generateUnifiedPatch(in.Path, applied.BaseContent, applied.NewContent),
				}
				if diffResult.HasFirstChanged {
					details.FirstChangedLine = diffResult.FirstChangedLine
				}
				detailsJSON, _ := json.Marshal(details)

				text := fmt.Sprintf("Successfully replaced %s in %s.", plural.Count(len(in.Edits), "block"), in.Path)
				return tool.Result{Content: msg.Blocks{msg.Text(text)}, Details: detailsJSON}, nil
			})
		},
	}
}

// errCode gives a stable, short error tag for the "Error code: %s" slot in
// pi's edit-access error messages, mirroring the FileError.code values
// nodejs.js maps ENOENT/EACCES/etc. to.
func errCode(err error) string {
	switch {
	case os.IsNotExist(err):
		return "not_found"
	case os.IsPermission(err):
		return "permission_denied"
	default:
		return "unknown"
	}
}
