package tui

import (
	"fmt"
	"os"
	"strings"
)

// Render what a tool call will actually change, for the permission prompt.
// Ported from change-preview.ts.
//
// Approving Edit(config.ts) without seeing the change is approving blind —
// the file path says nothing about whether the edit is a typo fix or
// deleting the file's contents. The prompt must show the change, not just
// name it.
//
// Diffs are computed from the *pending* arguments, before the tool runs,
// so this cannot reuse the edit tool's own output (which only exists
// afterwards).

// maxPreviewLines keeps a preview short enough to read; a 400-line diff is
// not a prompt.
const maxPreviewLines = 16

// EditOp is one pending replacement.
type EditOp struct {
	OldText string
	NewText string
}

func clipPreview(lines []string) []string {
	if len(lines) <= maxPreviewLines {
		return lines
	}
	hidden := len(lines) - maxPreviewLines
	out := append([]string{}, lines[:maxPreviewLines]...)
	out = append(out, Dim(fmt.Sprintf("  … %d more line(s)", hidden)))
	return out
}

// renderReplacement renders one replacement as removed lines followed by
// added lines.
//
// Deliberately not a line-by-line diff: OldText and NewText are exact
// strings the tool will match literally, so "these lines go, those arrive"
// is the honest rendering. Aligning them into a unified diff would imply a
// correspondence between old and new lines that the edit does not actually
// have.
//
// No surrounding context is shown, because the arguments do not contain
// any — the file is not read here, and reading it would preview a state
// that may differ from what the tool ultimately matches against.
func renderReplacement(op EditOp) []string {
	var lines []string
	for _, line := range firstN(strings.Split(op.OldText, "\n"), maxPreviewLines) {
		lines = append(lines, Red("  - "+line))
	}
	for _, line := range firstN(strings.Split(op.NewText, "\n"), maxPreviewLines) {
		lines = append(lines, Green("  + "+line))
	}
	return lines
}

// parseEdits accepts both the typed []EditOp callers can build directly
// and the []any-of-map[string]any shape decoded JSON arguments carry
// ("oldText"/"newText" keys), so RenderChangePreview works whether the
// caller constructed args itself or is passing through a decoded request.
func parseEdits(raw any) []EditOp {
	switch v := raw.(type) {
	case []EditOp:
		return v
	case []any:
		out := make([]EditOp, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			old, _ := m["oldText"].(string)
			newText, _ := m["newText"].(string)
			out = append(out, EditOp{OldText: old, NewText: newText})
		}
		return out
	default:
		return nil
	}
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// RenderChangePreview renders a human-readable preview of a pending
// change, or nil when the call has nothing to show beyond its arguments
// (bash, reads, MCP calls).
func RenderChangePreview(toolName string, args map[string]any) []string {
	tool := strings.ToLower(toolName)

	if tool == "edit" {
		edits := parseEdits(args["edits"])
		if len(edits) == 0 {
			return nil
		}
		var lines []string
		multi := len(edits) > 1
		for i, op := range edits {
			if multi {
				lines = append(lines, Dim(fmt.Sprintf("  edit %d of %d", i+1, len(edits))))
			}
			lines = append(lines, renderReplacement(op)...)
		}
		return clipPreview(lines)
	}

	if tool == "write" {
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)

		// Whether this creates or overwrites is the single most important
		// fact about a write, and it is invisible in the arguments alone.
		var existing string
		var exists bool
		if path != "" {
			if b, err := os.ReadFile(path); err == nil {
				existing = string(b)
				exists = true
			}
		}

		contentLines := strings.Split(content, "\n")

		if !exists {
			out := []string{Dim(fmt.Sprintf("  new file, %d line(s)", len(contentLines)))}
			for _, l := range contentLines {
				out = append(out, Green("  + "+l))
			}
			return clipPreview(out)
		}
		if existing == content {
			return []string{Dim("  no change")}
		}

		existingLines := strings.Split(existing, "\n")
		out := []string{Dim(fmt.Sprintf("  overwrites %d existing line(s)", len(existingLines)))}
		for _, l := range firstN(existingLines, 6) {
			out = append(out, Red("  - "+l))
		}
		for _, l := range firstN(contentLines, 6) {
			out = append(out, Green("  + "+l))
		}
		return clipPreview(out)
	}

	return nil
}
