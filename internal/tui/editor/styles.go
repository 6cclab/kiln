package editor

import "charm.land/lipgloss/v2"

// DefaultPlaceholder is the dim example text shown in an empty editor,
// ported from BorderedEditor's default in src/tui/app.ts (the constructor's
// `placeholder = 'Try "fix typecheck errors"'` parameter).
const DefaultPlaceholder = `Try "fix typecheck errors"`

// Styles is the small set of colours and glyphs the app injects into the
// editor, so this package never has to import the (not-yet-written) theme
// package at the tui root. It mirrors app.ts's BorderedEditor constructor
// arguments: a colour function (here, a lipgloss.Style) applied to the two
// rules and the marker, and the prompt glyph itself.
type Styles struct {
	// Marker is the prompt glyph rendered at the start of every content
	// line, e.g. "❯" (figures.pointer in the TS source).
	Marker string

	// Rule colours the top/bottom horizontal rules and the marker glyph.
	// app.ts uses a single `gray` function for both (`this.color(...)`
	// applied to the rule and to `g().userMark`), so one style covers both
	// here too.
	Rule lipgloss.Style

	// Placeholder colours the placeholder text spliced into an empty
	// editor's content line. app.ts wraps it in `dim(...)`.
	Placeholder lipgloss.Style
}
