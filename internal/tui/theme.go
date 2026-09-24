package tui

import (
	"os"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-isatty"
)

// Terminal styling.
//
// Ported from theme.ts. Every style helper routes through style(), which is
// a no-op when colour is disabled — that is what makes NO_COLOR and plain
// mode a single switch rather than a second rendering path, same as the TS
// original (there hand-rolled ANSI; here charm.land/lipgloss/v2, because a
// Go port has no equivalent to pi-tui's own visible-width helpers to keep a
// dependency-free implementation aligned with).

// colorEnabled honors NO_COLOR (informal standard), FORCE_COLOR, and
// whether stdout is a TTY, in that order — matching theme.ts's
// colorEnabled().
func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

var enabled = colorEnabled()

// SetColorEnabled disables styling globally: screen-reader mode, piped
// output, tests.
func SetColorEnabled(value bool) {
	enabled = value
}

// IsColorEnabled reports whether styling is currently applied.
func IsColorEnabled() bool {
	return enabled
}

func style(s lipgloss.Style) func(string) string {
	return func(text string) string {
		if !enabled {
			return text
		}
		return s.Render(text)
	}
}

var (
	// Dim renders text faint.
	Dim = style(lipgloss.NewStyle().Faint(true))
	// Bold renders text bold.
	Bold = style(lipgloss.NewStyle().Bold(true))
	// Italic renders text italic.
	Italic = style(lipgloss.NewStyle().Italic(true))
	// Strike renders text struck through.
	Strike = style(lipgloss.NewStyle().Strikethrough(true))

	// Red, Green, Yellow, Blue, Magenta, Cyan and Gray are the seven named
	// colours theme.ts exposes, using the same ANSI indices (31-36, 90).
	Red     = style(lipgloss.NewStyle().Foreground(lipgloss.Color("1")))
	Green   = style(lipgloss.NewStyle().Foreground(lipgloss.Color("2")))
	Yellow  = style(lipgloss.NewStyle().Foreground(lipgloss.Color("3")))
	Blue    = style(lipgloss.NewStyle().Foreground(lipgloss.Color("4")))
	Magenta = style(lipgloss.NewStyle().Foreground(lipgloss.Color("5")))
	Cyan    = style(lipgloss.NewStyle().Foreground(lipgloss.Color("6")))
	Gray    = style(lipgloss.NewStyle().Foreground(lipgloss.Color("8")))
)

// Suggestion is the light blue-purple Claude Code uses for the selected
// autocomplete row. Their palette calls it "suggestion" (rgb(177,185,249));
// 256-colour index 147 (rgb(175,175,255)) is the closest ANSI
// approximation available, matching theme.ts's choice exactly.
var Suggestion = style(lipgloss.NewStyle().Foreground(lipgloss.Color("147")))

// Glyphs is the table of decorative characters the transcript uses.
//
// Highest-risk detail in the whole UI: a wrong glyph is the most
// immediately visible parity failure, and these are wide/uncommon
// codepoints that not every font renders. ASCIIGlyphs is the fallback,
// also used by plain mode.
type Glyphs struct {
	// Call is the tool call marker, flush left.
	Call string
	// Result is the result continuation, indented under the call.
	Result string
	// Thinking marks a reasoning block: Claude Code's "∴".
	Thinking string
	// UserMark prefixes the user's own messages and the input prompt: "❯".
	UserMark string
	// Summary marks the line that closes a turn.
	Summary string

	TodoDone    string
	TodoPending string
	TodoActive  string

	Spinner []string
}

// UnicodeGlyphs is the default glyph table.
var UnicodeGlyphs = Glyphs{
	Call:        "⏺",
	Result:      "⎿",
	Thinking:    "∴",
	UserMark:    "❯",
	Summary:     "✳",
	TodoDone:    "☒",
	TodoPending: "☐",
	TodoActive:  "◐",
	// Claude Code's own frame set (darwin); subtle dots and asterisks
	// rather than the more visible braille cycle, which is what makes it
	// theirs.
	Spinner: []string{"·", "✢", "✳", "✶", "✻", "✽"},
}

// ASCIIGlyphs is the plain-mode fallback: no glyph outside the printable
// ASCII range.
var ASCIIGlyphs = Glyphs{
	Call:        "*",
	Result:      "\\",
	Thinking:    "*",
	UserMark:    ">",
	Summary:     "*",
	TodoDone:    "[x]",
	TodoPending: "[ ]",
	TodoActive:  "[~]",
	Spinner:     []string{"-", "\\", "|", "/"},
}

var glyphs = UnicodeGlyphs

// SetGlyphs replaces the active glyph table.
func SetGlyphs(next Glyphs) {
	glyphs = next
}

// G returns the active glyph table.
func G() Glyphs {
	return glyphs
}

// plain tracks screen-reader / plain mode, mirroring Claude Code's
// --ax-screen-reader: flat text, no decorative borders or animation.
// Doubles as the degraded mode for a bad SSH link or a dumb terminal.
var plain = false

// SetPlainMode toggles plain mode: colour off, ASCII glyphs, no decoration.
func SetPlainMode(on bool) {
	plain = on
	if on {
		SetColorEnabled(false)
	} else {
		SetColorEnabled(colorEnabled())
	}
	if on {
		SetGlyphs(ASCIIGlyphs)
	} else {
		SetGlyphs(UnicodeGlyphs)
	}
}

// IsPlain reports whether decorative glyphs should be avoided.
func IsPlain() bool {
	return plain
}
