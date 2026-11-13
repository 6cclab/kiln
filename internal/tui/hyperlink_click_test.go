package tui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestLinkAt: fullscreen takes the mouse, so kiln finds the link under a
// clicked cell itself, from the OSC 8 hyperlink glamour wrapped the label
// in, or from a bare URL in plain text.
func TestLinkAt(t *testing.T) {
	pr := "https://github.com/6cclab/ynm/pull/45"
	osc := "  See \x1b[36m" + ansi.SetHyperlink(pr) + "PR 45" + ansi.ResetHyperlink() + "\x1b[m and more"
	bare := "  ✓ opened https://example.com/a?b=1. done"
	cases := []struct {
		name string
		row  string
		col  int
		want string
	}{
		{"osc8 first cell", osc, 6, pr},
		{"osc8 last cell", osc, 10, pr},
		{"before osc8", osc, 5, ""},
		{"after osc8", osc, 11, ""},
		{"bare url start", bare, 11, "https://example.com/a?b=1"},
		{"bare url end", bare, 35, "https://example.com/a?b=1"},
		{"bare url trailing dot", bare, 36, ""},
		{"wide glyph before url", bare, 2, ""},
		{"past the row", bare, 200, ""},
		{"non-web scheme", ansi.SetHyperlink("file:///etc/passwd") + "x" + ansi.ResetHyperlink(), 0, ""},
		{"javascript in text", "javascript:alert(1)", 3, ""},
	}
	for _, c := range cases {
		if got := linkAt(c.row, c.col); got != c.want {
			t.Errorf("%s: linkAt(col %d) = %q, want %q", c.name, c.col, got, c.want)
		}
	}
}
