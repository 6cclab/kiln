package tui

import (
	"fmt"
	"github.com/andrepato/harness/internal/plural"
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
	out = append(out, Muted(fmt.Sprintf("  … %s more", plural.Count(hidden, "line"))))
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
		lines = append(lines, OnDiffDel(padToWidth(KilnRed("  − "+line), ruleWidth())))
	}
	for _, line := range firstN(strings.Split(op.NewText, "\n"), maxPreviewLines) {
		lines = append(lines, OnDiffAdd(padToWidth(KilnGreen("  + "+line), ruleWidth())))
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

// DiffFromEdit builds a ToolDiff (transcript.go) from one Edit's literal
// old/new text, for the committed result row after the edit has run —
// distinct from RenderChangePreview, which renders the same EditOp shape
// before the tool runs, for the permission prompt.
//
// Edit's arguments are two exact strings to match and replace, not a
// unified patch, so there is no hunk header to read a real starting line
// from. Old and new are paired line-for-line (line 1 of old against line 1
// of new, and so on): correct for the common case this harness's own edit
// tool is built around — a same-shaped replacement — and exactly what the
// reference capture shows (a one-line-for-one-line change numbered "1").
// A replacement that changes line count still renders every old line as
// removed and every new line as added, just without a claim that line N
// old corresponds to line N new beyond their shared position.
func DiffFromEdit(oldText, newText string, startLine int) *ToolDiff {
	if startLine <= 0 {
		startLine = 1
	}
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	d := &ToolDiff{Added: len(newLines), Removed: len(oldLines)}
	for i, l := range oldLines {
		d.Lines = append(d.Lines, DiffLine{Num: startLine + i, Sign: '-', Text: l})
	}
	for i, l := range newLines {
		d.Lines = append(d.Lines, DiffLine{Num: startLine + i, Sign: '+', Text: l})
	}
	return d
}

// changePreviewHeader renders the kiln "edit" label rule above a pending
// diff preview: blue label, filename meta when the path is known. Per the
// block anatomy, a preview with no filename keeps its header minimal (no
// rule at all) rather than drawing a blue rule with nothing to point at.
func changePreviewHeader(path string) []string {
	if path == "" {
		return nil
	}
	return []string{labelRule("edit", KilnBlue, path, ruleWidth())}
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
		path, _ := args["path"].(string)
		var lines []string
		multi := len(edits) > 1
		for i, op := range edits {
			if multi {
				lines = append(lines, Muted(fmt.Sprintf("  edit %d of %d", i+1, len(edits))))
			}
			lines = append(lines, renderReplacement(op)...)
		}
		return append(changePreviewHeader(path), clipPreview(lines)...)
	}

	if tool == "write" {
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		header := changePreviewHeader(path)

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
			out := []string{Muted(fmt.Sprintf("  new file, %s", plural.Count(len(contentLines), "line")))}
			for _, l := range contentLines {
				out = append(out, OnDiffAdd(padToWidth(KilnGreen("  + "+l), ruleWidth())))
			}
			return append(header, clipPreview(out)...)
		}
		if existing == content {
			return append(header, Muted("  no change"))
		}

		existingLines := strings.Split(existing, "\n")
		out := []string{Muted(fmt.Sprintf("  overwrites %s", plural.Count(len(existingLines), "existing line")))}
		for _, l := range firstN(existingLines, 6) {
			out = append(out, OnDiffDel(padToWidth(KilnRed("  − "+l), ruleWidth())))
		}
		for _, l := range firstN(contentLines, 6) {
			out = append(out, OnDiffAdd(padToWidth(KilnGreen("  + "+l), ruleWidth())))
		}
		return append(header, clipPreview(out)...)
	}

	return nil
}
