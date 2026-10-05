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
		"For text files, output is truncated to %d lines or %dKB (whichever is hit first). Files larger than %dKB are refused "+
		"unless offset/limit is given; use offset/limit for large files. When you need the full file, continue with offset until complete.",
	execenv.DefaultMaxLines, execenv.DefaultMaxBytes/1024, execenv.ReadWholeFileCap/1024,
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
//
// Two reads never load a whole file before any cap looks at it (core
// audit H1): an untargeted read (no offset/limit) is refused outright
// above execenv.ReadWholeFileCap, matching Claude Code's own Read tool;
// an offset/limit read instead streams the file a line at a time
// (execenv.LineScanner) regardless of size, so paging through part of a
// multi-GB file never requires reading the rest of it into memory — only
// counting past it to report how many lines remain.
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

			info, err := env.Stat(absolutePath)
			if err != nil {
				errText := err.Error()
				if hint := env.DidYouMeanHint(absolutePath); hint != "" {
					errText += " " + hint
				}
				return tool.Errorf("%s", errText), nil
			}
			if info.Kind == execenv.KindDirectory {
				return tool.Errorf("Cannot read %s: it is a directory.", in.Path), nil
			}

			windowed := in.Offset != nil || in.Limit != nil
			if !windowed {
				if info.Size > execenv.ReadWholeFileCap {
					return tool.Errorf(
						"%s is %s, more than the %s limit for a whole-file read. Use offset/limit to read part of it, or bash (e.g. sed/head/tail) for the rest.",
						in.Path, execenv.FormatSize(int(info.Size)), execenv.FormatSize(int(execenv.ReadWholeFileCap)),
					), nil
				}
				return readWholeFile(env, absolutePath, in.Path)
			}
			return readFileWindow(env, absolutePath, in.Path, in.Offset, in.Limit)
		},
	}
}

// readWholeFile is the untargeted-read path: the whole file (already
// known to be at or under execenv.ReadWholeFileCap) is read once, sniffed
// for an image, and otherwise truncated to the tool's usual output caps.
func readWholeFile(env *execenv.Env, absolutePath, displayPath string) (tool.Result, error) {
	bytes, err := env.ReadFile(absolutePath)
	if err != nil {
		errText := err.Error()
		if hint := env.DidYouMeanHint(absolutePath); hint != "" {
			errText += " " + hint
		}
		return tool.Errorf("%s", errText), nil
	}

	if mimeType := detectSupportedImageMimeType(bytes); mimeType != "" {
		return tool.Result{Content: msg.Blocks{
			msg.Text(fmt.Sprintf("Read image file [%s]", mimeType)),
			msg.Image(mimeType, encodeImageBase64(bytes)),
		}}, nil
	}

	textContent := string(bytes)
	truncation := execenv.TruncateHead(textContent, execenv.TruncateOptions{})
	var outputText string
	var details *readDetails

	switch {
	case truncation.FirstLineExceedsLimit:
		firstLine := strings.SplitN(textContent, "\n", 2)[0]
		outputText = fmt.Sprintf(
			"[Line 1 is %s, exceeds %s limit. Use bash: sed -n '1p' %s | head -c %d]",
			execenv.FormatSize(len(firstLine)), execenv.FormatSize(execenv.DefaultMaxBytes),
			displayPath, execenv.DefaultMaxBytes,
		)
		details = &readDetails{Truncation: toTruncationDetails(truncation)}
	case truncation.Truncated:
		endLineDisplay := truncation.OutputLines
		nextOffset := endLineDisplay + 1
		outputText = truncation.Content
		if truncation.TruncatedBy == execenv.TruncatedByLines {
			outputText += fmt.Sprintf("\n\n[Showing lines 1-%d of %d. Use offset=%d to continue.]",
				endLineDisplay, truncation.TotalLines, nextOffset)
		} else {
			outputText += fmt.Sprintf("\n\n[Showing lines 1-%d of %d (%s limit). Use offset=%d to continue.]",
				endLineDisplay, truncation.TotalLines, execenv.FormatSize(execenv.DefaultMaxBytes), nextOffset)
		}
		details = &readDetails{Truncation: toTruncationDetails(truncation)}
	default:
		outputText = truncation.Content
	}

	detailsJSON, _ := json.Marshal(details)
	if details == nil {
		detailsJSON = nil
	}
	return tool.Result{Content: msg.Blocks{msg.Text(outputText)}, Details: detailsJSON}, nil
}

// readFileWindow is the offset/limit path: it streams the file a line at
// a time (execenv.LineScanner) rather than reading it whole, collecting
// only the requested (and default-capped) window's content in memory
// while still counting every line up to EOF, so the usual "N more lines,
// use offset=X" and "of TOTAL lines" messages stay accurate on a file far
// larger than execenv.ReadWholeFileCap.
func readFileWindow(env *execenv.Env, absolutePath, displayPath string, offset, limit *int) (tool.Result, error) {
	scanner, closer, err := env.OpenLineScanner(absolutePath, execenv.DefaultMaxBytes)
	if err != nil {
		errText := err.Error()
		if hint := env.DidYouMeanHint(absolutePath); hint != "" {
			errText += " " + hint
		}
		return tool.Errorf("%s", errText), nil
	}
	defer closer.Close()

	startLine := 0
	if offset != nil {
		startLine = *offset - 1
		if startLine < 0 {
			startLine = 0
		}
	}
	startLineDisplay := startLine + 1
	endLine := -1 // exclusive; -1 means "no user limit"
	if limit != nil {
		endLine = startLine + *limit
	}

	var collected []string
	collectedBytes := 0
	truncated := false
	truncatedByLines := false
	firstLineExceeds := false
	var firstLineBytes int
	stoppedCollecting := false
	sawStart := false
	sawFullUserWindow := false
	totalFileLines := 0

	for {
		line, ok, nextErr := scanner.Next()
		if nextErr != nil {
			return tool.Errorf("%s: %s", displayPath, nextErr), nil
		}
		if !ok {
			break
		}
		idx := totalFileLines
		totalFileLines++
		if idx < startLine {
			continue
		}
		sawStart = true
		if endLine >= 0 && idx >= endLine {
			continue
		}
		if endLine >= 0 && idx == endLine-1 && !stoppedCollecting {
			sawFullUserWindow = true
		}
		if stoppedCollecting {
			continue
		}
		if len(collected) == 0 && line.Oversize {
			firstLineExceeds = true
			firstLineBytes = line.ByteLen
			stoppedCollecting = true
			continue
		}
		addBytes := line.ByteLen
		if len(collected) > 0 {
			addBytes++ // the "\n" join separator
		}
		switch {
		case len(collected) >= execenv.DefaultMaxLines:
			truncated = true
			truncatedByLines = true
			stoppedCollecting = true
		case collectedBytes+addBytes > execenv.DefaultMaxBytes:
			truncated = true
			stoppedCollecting = true
		default:
			collected = append(collected, line.Content)
			collectedBytes += addBytes
		}
	}

	if !sawStart {
		return tool.Errorf("Offset %d is beyond end of file (%d lines total)", derefInt(offset), totalFileLines), nil
	}

	var outputText string
	var details *readDetails
	switch {
	case firstLineExceeds:
		outputText = fmt.Sprintf(
			"[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			startLineDisplay, execenv.FormatSize(firstLineBytes), execenv.FormatSize(execenv.DefaultMaxBytes),
			startLineDisplay, displayPath, execenv.DefaultMaxBytes,
		)
		details = &readDetails{Truncation: &truncationDetails{
			Truncated: true, TruncatedBy: execenv.TruncatedByBytes, TotalLines: totalFileLines,
			FirstLineExceedsLimit: true,
			MaxLines:              execenv.DefaultMaxLines, MaxBytes: execenv.DefaultMaxBytes,
		}}
	case truncated:
		endLineDisplay := startLineDisplay + len(collected) - 1
		nextOffset := endLineDisplay + 1
		outputText = strings.Join(collected, "\n")
		truncatedBy := execenv.TruncatedByBytes
		if truncatedByLines {
			truncatedBy = execenv.TruncatedByLines
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, nextOffset)
		} else {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, execenv.FormatSize(execenv.DefaultMaxBytes), nextOffset)
		}
		details = &readDetails{Truncation: &truncationDetails{
			Truncated: true, TruncatedBy: truncatedBy, TotalLines: totalFileLines,
			OutputLines: len(collected), MaxLines: execenv.DefaultMaxLines, MaxBytes: execenv.DefaultMaxBytes,
		}}
	case limit != nil && sawFullUserWindow && totalFileLines > endLine:
		remaining := totalFileLines - endLine
		nextOffset := endLine + 1
		outputText = fmt.Sprintf("%s\n\n[%d more lines in file. Use offset=%d to continue.]",
			strings.Join(collected, "\n"), remaining, nextOffset)
	default:
		outputText = strings.Join(collected, "\n")
	}

	detailsJSON, _ := json.Marshal(details)
	if details == nil {
		detailsJSON = nil
	}
	return tool.Result{Content: msg.Blocks{msg.Text(outputText)}, Details: detailsJSON}, nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
