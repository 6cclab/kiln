package editor

import "charm.land/lipgloss/v2"

// DefaultPlaceholder is the dim example text shown in an empty, idle editor
// (docs/kiln-design-handoff/README.md "Interactions"). SetPlaceholder
// overrides it for other states (busy, waiting on a prompt).
const DefaultPlaceholder = `describe a task · / for commands · @ to add files`

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

	// RuleChar is the top/bottom rule's fill character: "─" normally, "-"
	// in plain mode (--ax-screen-reader). This package cannot call
	// tui.IsPlain itself (tui imports editor, not the other way around),
	// so the caller (app.go's editorStyles) passes the answer down instead
	// — view.go's rule() falls back to "─" when this is left unset, so
	// nothing outside app.go has to change. Fixed at "─" unconditionally
	// used to be exactly the box-drawing leak plain mode was supposed to
	// have none of (defect *screen-reader-mode-leaves-box-drawing-rules).
	RuleChar string
}
