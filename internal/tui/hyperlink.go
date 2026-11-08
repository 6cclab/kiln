package tui

import "github.com/charmbracelet/x/ansi"

// Hyperlink wraps label in an OSC 8 terminal hyperlink to url so a supporting
// terminal makes label clickable. Terminals that don't support OSC 8 ignore the
// escape and show label unchanged, and the escape is zero-width (ansi.StringWidth
// and ansi.Wrap, the engines behind VisibleWidth and ansiWrap, both account for
// it), so a link never disturbs layout or wrapping. A no-op when styling is
// disabled (NO_COLOR, plain/screen-reader mode, piped output) — the same
// `enabled` switch as every other style helper — so escapes never reach output
// that should carry none. An empty url returns label unchanged.
//
// Assistant text (markdown) does not go through here: glamour already emits OSC
// 8 for both bare URLs and [text](url) links. This is for kiln's own renderers
// that build a link outside glamour, i.e. a mod <a href> element (renderLink).
func Hyperlink(url, label string) string {
	if !enabled || url == "" {
		return label
	}
	return ansi.SetHyperlink(url) + label + ansi.ResetHyperlink()
}
