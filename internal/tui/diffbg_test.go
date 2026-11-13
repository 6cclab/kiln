package tui

import (
	"strings"
	"testing"
)

// assertOnBackground fails unless every cell of row is drawn on bg. A
// background wrapped around coloured spans used to stop at the first span's
// reset, so a diff row was tinted behind its line number only (finding
// 20261112T220956Z-diff-row-background-reset).
func assertOnBackground(t *testing.T, name, row string, bg func(string) string, width int) {
	t.Helper()
	on, _, _ := strings.Cut(bg("\x00"), "\x00")
	param := strings.TrimSuffix(strings.TrimPrefix(on, "\x1b["), "m")
	if param == "" {
		t.Fatalf("%s: background token renders no SGR (%q)", name, on)
	}
	cs := cells(row)
	if len(cs) != width {
		t.Errorf("%s: %d cells, want %d", name, len(cs), width)
	}
	for i, c := range cs {
		if !strings.Contains(c.style, param) {
			t.Errorf("%s: cell %d (%q) is drawn in %q, off the %q background\nrow %q", name, i, c.r, c.style, param, row)
			return
		}
	}
}

func TestDiffRows_BackgroundSpansTheRow(t *testing.T) {
	withRenderEnv(t, 80)
	width := ruleWidth()
	rows := RenderDiffLines([]DiffLine{
		{Num: 2, Sign: '-', Text: "  return a - b"},
		{Num: 2, Sign: '+', Text: "  return a + b"},
	})
	assertOnBackground(t, "removed row", rows[0], OnDiffDel, width)
	assertOnBackground(t, "added row", rows[1], OnDiffAdd, width)

	header := diffHeaderRow("src/math.js", &ToolDiff{Added: 1, Removed: 1, NewFile: true}, width)
	assertOnBackground(t, "diff header", header, OnPanel, width)

	preview := renderReplacement(EditOp{OldText: "a - b", NewText: "a + b"})
	assertOnBackground(t, "preview removed", preview[0], OnDiffDel, width)
	assertOnBackground(t, "preview added", preview[1], OnDiffAdd, width)
}
