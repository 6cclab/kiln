package tui

import "strings"

// shortcutColumns are the `?` panel's three columns, in Claude Code's order
// (docs/claude-code-reference.md §6, shortcuts.txt). Only entries the
// harness implements are listed; dead entries are omitted.
var shortcutColumns = [3][]string{
	{
		"! for shell mode",
		"/ for commands",
		"@ for file paths",
	},
	{
		"double tap esc to clear input",
		"shift + tab to auto-accept edits",
		"ctrl + o for verbose output",
		"shift + ⏎ for newline",
	},
	{},
}

// shortcutColumnStarts are the columns each list starts at in shortcuts.txt
// (2, 26, 61).
var shortcutColumnStarts = [3]int{2, 26, 61}

// RenderShortcuts renders the shortcuts panel shown under the mode line
// while `?` is active, one row per line of the tallest column, each column
// starting at its reference offset. Rows are fitted to width.
func RenderShortcuts(width int) []string {
	rows := 0
	for _, col := range shortcutColumns {
		if len(col) > rows {
			rows = len(col)
		}
	}
	out := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		var b strings.Builder
		for c, col := range shortcutColumns {
			if i >= len(col) {
				continue
			}
			for b.Len() < shortcutColumnStarts[c] {
				b.WriteByte(' ')
			}
			b.WriteString(col[i])
		}
		out = append(out, FitStatus(Muted(b.String()), width))
	}
	return out
}
