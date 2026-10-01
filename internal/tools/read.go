package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

var readParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Path to the file to read (relative or absolute)"},
		"offset": {"type": "number", "description": "Line number to start reading from (1-indexed)"},
		"limit": {"type": "number", "description": "Maximum number of lines to read"}
	},
	"required": ["path"]
}`)

var readDescription = fmt.Sprintf(
	"Read the contents of a file. Supports text files and images (jpg, png, gif, webp, bmp). Images are sent as attachments. "+
		"For text files, output is truncated to %d lines or %dKB (whichever is hit first). Use offset/limit for large files. "+
		"When you need the full file, continue with offset until complete.",
	execenv.DefaultMaxLines, execenv.DefaultMaxBytes/1024,
)

type readArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

// readDetails is the machine-readable payload alongside a read result,
// mirroring read.js's ReadToolDetails.
type readDetails struct {
	Truncation *truncationDetails `json:"truncation,omitempty"`
}

// ReadTool builds the read built-in, mirroring read.js's createReadTool:
// path resolution retries pi's filename-quirk variants
// (resolveReadToolPath), image files (by magic bytes, not extension) come
// back as an ImageContent block instead of text, and text files apply
// offset/limit before truncateHead bounds the result — offset/limit
// windowing happens first so a user-requested slice, not just the file's
// first N lines, is what gets truncated.
func ReadTool(env *execenv.Env) *tool.Tool {
	return &tool.Tool{
		Name:        "read",
		Label:       "read",
		Description: readDescription,
		Parameters:  readParameters,
		Execute: func(_ context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var in readArgs
			if err := json.Unmarshal(args, &in); err != nil {
				return tool.Errorf("invalid arguments: %s", err), nil
			}
			absolutePath := resolveReadToolPath(env, in.Path)
			if refusal, refused := refuseVolPath(absolutePath, in.Path); refused {
				return refusal, nil
			}
			bytes, err := env.ReadFile(absolutePath)
			if err != nil {
				return tool.Errorf("%s", err), nil
			}

			if mimeType := detectSupportedImageMimeType(bytes); mimeType != "" {
				return tool.Result{Content: msg.Blocks{
					msg.Text(fmt.Sprintf("Read image file [%s]", mimeType)),
					msg.Image(mimeType, encodeImageBase64(bytes)),
				}}, nil
			}

			textContent := string(bytes)
			allLines := strings.Split(textContent, "\n")
			totalFileLines := len(allLines)
			startLine := 0
			if in.Offset != nil {
				startLine = *in.Offset - 1
				if startLine < 0 {
					startLine = 0
				}
			}
			startLineDisplay := startLine + 1
			if startLine >= len(allLines) {
				return tool.Errorf("Offset %d is beyond end of file (%d lines total)", derefInt(in.Offset), len(allLines)), nil
			}

			var selectedContent string
			var userLimitedLines *int
			if in.Limit != nil {
				endLine := startLine + *in.Limit
				if endLine > len(allLines) {
					endLine = len(allLines)
				}
				selectedContent = strings.Join(allLines[startLine:endLine], "\n")
				n := endLine - startLine
				userLimitedLines = &n
			} else {
				selectedContent = strings.Join(allLines[startLine:], "\n")
			}

			truncation := execenv.TruncateHead(selectedContent, execenv.TruncateOptions{})
			var outputText string
			var details *readDetails

			switch {
			case truncation.FirstLineExceedsLimit:
				firstLineSize := execenv.FormatSize(len(allLines[startLine]))
				outputText = fmt.Sprintf(
					"[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
					startLineDisplay, firstLineSize, execenv.FormatSize(execenv.DefaultMaxBytes),
					startLineDisplay, in.Path, execenv.DefaultMaxBytes,
				)
				details = &readDetails{Truncation: toTruncationDetails(truncation)}
			case truncation.Truncated:
				endLineDisplay := startLineDisplay + truncation.OutputLines - 1
				nextOffset := endLineDisplay + 1
				outputText = truncation.Content
				if truncation.TruncatedBy == execenv.TruncatedByLines {
					outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
						startLineDisplay, endLineDisplay, totalFileLines, nextOffset)
				} else {
					outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
						startLineDisplay, endLineDisplay, totalFileLines, execenv.FormatSize(execenv.DefaultMaxBytes), nextOffset)
				}
				details = &readDetails{Truncation: toTruncationDetails(truncation)}
			case userLimitedLines != nil && startLine+*userLimitedLines < len(allLines):
				remaining := len(allLines) - (startLine + *userLimitedLines)
				nextOffset := startLine + *userLimitedLines + 1
				outputText = fmt.Sprintf("%s\n\n[%d more lines in file. Use offset=%d to continue.]",
					truncation.Content, remaining, nextOffset)
			default:
				outputText = truncation.Content
			}

			detailsJSON, _ := json.Marshal(details)
			if details == nil {
				detailsJSON = nil
			}
			return tool.Result{Content: msg.Blocks{msg.Text(outputText)}, Details: detailsJSON}, nil
		},
	}
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
