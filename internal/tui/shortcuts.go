package tui

import "strings"

// shortcut is one `?` panel entry: the key, drawn amber like the banner's
// shortcut row, and what it does, dim.
type shortcut struct{ key, does string }

// shortcutColumns are the `?` panel's columns: input prefixes on the left,
// keys on the right. Only what kiln implements is listed (keys.go and
// inputmodes.go).
var shortcutColumns = [2][]shortcut{
	{
		{"!", "run a shell command"},
		{"/", "commands"},
		{"@", "add files"},
		{"#", "save a memory note"},
		{"shift+⏎", "new line"},
	},
	{
		{"⇧⇥", "cycle permission mode"},
		{"esc esc", "rewind the conversation"},
		{"ctrl+o", "verbose transcript"},
		{"ctrl+f", "fullscreen or inline"},
		{"ctrl+c ×2", "exit"},
	},
}

// shortcutGap separates the two columns.
const shortcutGap = 4

// RenderShortcuts renders the shortcuts panel shown under the mode line
// while `?` is active: two columns, each a key column padded to its widest
// key and a description. Widths are counted in cells, so a multi-byte glyph
// (⇧, ⏎) does not push its row out of line. Rows are fitted to width.
func RenderShortcuts(width int) []string {
	keyWidth := func(col []shortcut) int {
		w := 0
		for _, s := range col {
			w = max(w, VisibleWidth(s.key))
		}
		return w
	}
	cellWidth := func(col []shortcut, kw int) int {
		w := 0
		for _, s := range col {
			w = max(w, kw+2+VisibleWidth(s.does))
		}
		return w
	}
	rows := max(len(shortcutColumns[0]), len(shortcutColumns[1]))
	kw0, kw1 := keyWidth(shortcutColumns[0]), keyWidth(shortcutColumns[1])
	leftWidth := cellWidth(shortcutColumns[0], kw0)

	cell := func(s shortcut, kw int) string {
		return KilnAmber(s.key) + strings.Repeat(" ", kw-VisibleWidth(s.key)+2) + Muted(s.does)
	}
	out := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		var b strings.Builder
		b.WriteString("  ")
		used := 0
		if i < len(shortcutColumns[0]) {
			s := shortcutColumns[0][i]
			b.WriteString(cell(s, kw0))
			used = kw0 + 2 + VisibleWidth(s.does)
		}
		if i < len(shortcutColumns[1]) {
			b.WriteString(strings.Repeat(" ", leftWidth-used+shortcutGap))
			b.WriteString(cell(shortcutColumns[1][i], kw1))
		}
		out = append(out, FitStatus(b.String(), width))
	}
	return out
}
