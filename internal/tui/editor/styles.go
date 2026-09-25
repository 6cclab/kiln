package editor

import "charm.land/lipgloss/v2"

// DefaultPlaceholder is the dim example text shown in an empty editor. The
// harness has no rotating-tip infrastructure of its own yet (nothing else
// in this package or its caller picks a tip at random or on a timer); this
// is the one placeholder shown, and it must read exactly as Claude Code's
// own first tip does (docs/claude-code-reference.md §2,
// testdata/reference/claude-code/startup-default-home.txt row 9: `❯ Try "how
// do I log an error?"`).
const DefaultPlaceholder = `Try "how do I log an error?"`

// Styles is the small set of colours and glyphs the app injects into the
// editor, so this package never has to import the (not-yet-written) theme
// package at the tui root. It mirrors app.ts's BorderedEditor constructor
// arguments: a colour function (here, a lipgloss.Style) applied to the two
// rules and the marker, and the prompt glyph itself.
type Styles struct {
	// Marker is the prompt glyph rendered at the start of every content
	// line, e.g. "❯" (figures.pointer in the TS source).
	Marker string

	// MarkerStyle colours the marker glyph itself. Kiln colours the
	// prompt glyph amber, distinct from the rule colour (rule-strong),
	// so it is its own field rather than sharing Rule the way the
	// Claude Code original did (a single `gray` function for both).
	MarkerStyle lipgloss.Style

	// Rule colours the top/bottom horizontal rules.
	Rule lipgloss.Style

	// Placeholder colours the placeholder text spliced into an empty
	// editor's content line. app.ts wraps it in `dim(...)`.
	Placeholder lipgloss.Style
}
