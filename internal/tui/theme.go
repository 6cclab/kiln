package tui

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"strings"

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
// autocomplete row and other accent text (e.g. the `/model` mention in the
// startup tip): rgb(177,185,249), read directly off the SGR in force at
// that text in testdata/reference/claude-code/manual-session.rec (offset
// ~1850, "IN:/mod" autocomplete list) and reused verbatim here since the
// kiln design palette (Layout 1b "Ruled"). All 24-bit truecolor; kiln
// always runs in a truecolor-capable profile. These hex values are the
// design tokens from the kiln handoff (design_handoff_kiln_tui/README.md).
// The named helpers below are the single source of truth; the legacy
// token names further down are aliases mapped onto these so existing call
// sites recolor to kiln without churn.
const (
	hexInk        = "#ece4d4" // primary text
	hexDim        = "#a39781" // secondary text, labels, meta, statusline
	hexFaint      = "#7d7262" // line numbers, todo glyph, unselected keys
	hexAmber      = "#e9a64b" // accent: prompt, running, `you`, approvals, KILN
	hexGreen      = "#9bc46e" // success, additions, done
	hexRed        = "#e5765d" // errors, removals
	hexBlue       = "#86b4d4" // edit label, plan mode
	hexViolet     = "#c3a3d6" // context "tools" segment
	hexRule       = "#2f2920" // block label hairlines, empty meter, diff borders
	hexRuleStrong = "#3a3228" // input box rules
	hexPanel      = "#1c1813" // diff header background
	hexRaise      = "#241f18" // user message / $cmd / selected-row background
	hexDiffAddBg  = "#232619" // diff "+" line background
	hexDiffDelBg  = "#2f1c15" // diff "−" line background
	hexBarEmpty   = "#3f372c" // subagents panel: progress bar empty cell
)

// Kiln foreground helpers. Ink, Faint and the named accents are the design's
// own fixed hexes — only the *surface* tokens (hairlines, raised/panel
// backgrounds, diff tints) are background-aware; see SetTerminalBackground.
var (
	Ink       = style(lipgloss.NewStyle().Foreground(lipgloss.Color(hexInk)))
	Faint     = style(lipgloss.NewStyle().Foreground(lipgloss.Color(hexFaint)))
	KilnAmber = style(lipgloss.NewStyle().Foreground(lipgloss.Color(hexAmber)))
	KilnGreen = style(lipgloss.NewStyle().Foreground(lipgloss.Color(hexGreen)))
	KilnRed   = style(lipgloss.NewStyle().Foreground(lipgloss.Color(hexRed)))
	KilnBlue  = style(lipgloss.NewStyle().Foreground(lipgloss.Color(hexBlue)))
	Violet    = style(lipgloss.NewStyle().Foreground(lipgloss.Color(hexViolet)))
)

// Rule is the hairline colour for block label rules and diff borders.
// RuleStrong is the input box's rules (brighter than Rule). BarEmpty is the
// subagents panel's progress-bar empty-cell colour (docs/kiln-design-
// handoff/README.md "agents" row: "empty #3f372c") — a design token distinct
// from Rule (the general hairline/empty-meter colour) because the handoff
// calls out a lighter shade for this one bar specifically.
//
// These, and the background helpers just below, are surface tokens: they
// assume the design's own background (#14110d) by default, but every real
// terminal has its own background, and a hairline/tint tuned only for
// #14110d can be invisible against a lighter or differently-hued one (empty
// meter cells, input/banner rules disappearing entirely). SetTerminalBackground
// recomputes all of them, and RuleColour below, as blends between the
// terminal's actual background and the design's ink/accent colours, so they
// stay visibly a "surface" against whatever background the terminal really
// has. They are package-level func vars (not consts wrapped in style() at
// var-init time) precisely so SetTerminalBackground can rebuild them.
var (
	Rule       func(string) string
	RuleStrong func(string) string
	BarEmpty   func(string) string
)

// Kiln background helpers. Callers pad the text to the intended width
// before wrapping so the tint spans the whole row (inline mode does not
// own the terminal's global background, so only these local tints apply).
// See Rule's doc comment above: these are surface tokens too.
var (
	// OnRaise tints a span with the raised surface (user message, $cmd,
	// selected rows).
	OnRaise func(string) string
	// OnPanel tints a span with the panel surface (diff header).
	OnPanel func(string) string
	// OnDiffAdd / OnDiffDel tint added / removed diff line backgrounds.
	OnDiffAdd func(string) string
	OnDiffDel func(string) string
)

// hexDesignBg is the kiln design's own background (docs/kiln-design-
// handoff/README.md's palette, "#14110d") — the reference every surface
// token above is defined relative to, and the value SetTerminalBackground
// compares an actual terminal background against to decide whether to keep
// the design's exact hexes unchanged.
const hexDesignBg = "#14110d"

// designBgNearThreshold bounds how far a detected terminal background may
// be (per RGB channel's simple Euclidean distance, 0-441.7 range) from
// hexDesignBg and still count as "the design background" — small enough to
// absorb a terminal's own gamma/rounding on that exact colour, far enough
// that an actually different (if also dark) background still gets
// recomputed tokens. 24 is roughly a 10% per-channel tolerance.
const designBgNearThreshold = 24.0

func init() {
	resetSurfaceTokensToDesign()
}

// resetSurfaceTokensToDesign rebuilds every surface token (Rule, RuleStrong,
// BarEmpty, OnRaise, OnPanel, OnDiffAdd, OnDiffDel) from the design's own
// fixed hexes, the state at startup before any SetTerminalBackground call
// and the state SetTerminalBackground restores when the reported background
// is within designBgNearThreshold of hexDesignBg.
func resetSurfaceTokensToDesign() {
	setSurfaceTokens(hexRule, hexRuleStrong, hexBarEmpty, hexRaise, hexPanel, hexDiffAddBg, hexDiffDelBg)
}

func setSurfaceTokens(rule, ruleStrong, barEmpty, raise, panel, diffAdd, diffDel string) {
	Rule = style(lipgloss.NewStyle().Foreground(lipgloss.Color(rule)))
	RuleStrong = style(lipgloss.NewStyle().Foreground(lipgloss.Color(ruleStrong)))
	BarEmpty = style(lipgloss.NewStyle().Foreground(lipgloss.Color(barEmpty)))
	OnRaise = style(lipgloss.NewStyle().Background(lipgloss.Color(raise)))
	OnPanel = style(lipgloss.NewStyle().Background(lipgloss.Color(panel)))
	OnDiffAdd = style(lipgloss.NewStyle().Background(lipgloss.Color(diffAdd)))
	OnDiffDel = style(lipgloss.NewStyle().Background(lipgloss.Color(diffDel)))
}

// rgb8 is an 8-bit-per-channel colour, the precision every hex token and
// every blend computation here works in (the design's own palette is
// specified as 8-bit hex, and terminal background reports are effectively
// the same precision once truncated from whatever backing depth the
// terminal answers with).
type rgb8 struct{ r, g, b uint8 }

// parseHex parses a "#rrggbb" string. Panics on malformed input — every
// caller in this file passes one of this package's own hex consts, never
// unvalidated input, so a malformed one is a programming error to catch at
// build/test time, not a runtime condition to handle gracefully.
func parseHex(s string) rgb8 {
	s = strings.TrimPrefix(s, "#")
	var r, g, b uint8
	if _, err := fmt.Sscanf(s, "%02x%02x%02x", &r, &g, &b); err != nil {
		panic(fmt.Sprintf("theme: malformed hex color %q: %v", s, err))
	}
	return rgb8{r, g, b}
}

// toRGB8 downsamples an arbitrary image/color.Color (bubbletea's
// tea.BackgroundColorMsg reports one, typically at 16 bits/channel from an
// OSC 11 reply) to this package's 8-bit precision.
func toRGB8(c color.Color) rgb8 {
	r, g, b, _ := c.RGBA()
	return rgb8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)} //nolint:gosec
}

func (c rgb8) hex() string {
	return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b)
}

// distance is the Euclidean distance between two colours' RGB channels
// (0-441.67 range), used only to decide "close enough to the design
// background to keep its exact hexes" — not a perceptual colour-difference
// metric, just a cheap, deterministic proximity check.
func (c rgb8) distance(o rgb8) float64 {
	dr := float64(c.r) - float64(o.r)
	dg := float64(c.g) - float64(o.g)
	db := float64(c.b) - float64(o.b)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

// mix blends from bg toward fg by t (0 = bg, 1 = fg), per channel, rounded
// to the nearest 8-bit value. This is a plain linear RGB blend (not a
// perceptual colour space) — the design handoff specifies each surface
// token as "mix(bg, x, t)" in exactly these terms.
func mix(bg, fg rgb8, t float64) rgb8 {
	blend := func(a, b uint8) uint8 {
		v := float64(a) + (float64(b)-float64(a))*t
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(math.Round(v))
	}
	return rgb8{blend(bg.r, fg.r), blend(bg.g, fg.g), blend(bg.b, fg.b)}
}

// SetTerminalBackground recomputes every surface token (Rule, RuleStrong,
// BarEmpty, OnRaise, OnPanel, OnDiffAdd, OnDiffDel and RuleColour, its
// legacy alias) as blends between the terminal's actual background and
// ink/green/red, so hairlines and empty-meter cells stay visible against a
// real terminal background instead of assuming the design's own #14110d
// (docs/kiln-design-handoff/README.md's palette) — see Rule's doc comment
// above for why. Blend weights, straight from the design handoff:
//
//	rule        = mix(bg, ink, 0.16)
//	rule-strong = mix(bg, ink, 0.24)
//	raise       = mix(bg, ink, 0.09)
//	panel       = mix(bg, ink, 0.06)
//	bar-empty   = mix(bg, ink, 0.22)
//	diff-add    = mix(bg, green, 0.12)
//	diff-del    = mix(bg, red, 0.14)
//
// When bg is within designBgNearThreshold of the design's own background,
// this instead restores the design's exact hexes unchanged (so a terminal
// that happens to already run near the design palette, and every PTY golden
// pinned to it, sees no difference at all).
//
// A no-op when colour is disabled (NO_COLOR, --ax-screen-reader, a
// non-TTY): style()'s own enabled check already makes every helper here an
// identity function in that case, so recomputing their hexes would be dead
// work masking nothing.
func SetTerminalBackground(c color.Color) {
	if !enabled || c == nil {
		return
	}
	bg := toRGB8(c)
	design := parseHex(hexDesignBg)
	if bg.distance(design) <= designBgNearThreshold {
		resetSurfaceTokensToDesign()
		return
	}
	ink := parseHex(hexInk)
	green := parseHex(hexGreen)
	red := parseHex(hexRed)
	setSurfaceTokens(
		mix(bg, ink, 0.16).hex(),
		mix(bg, ink, 0.24).hex(),
		mix(bg, ink, 0.22).hex(),
		mix(bg, ink, 0.09).hex(),
		mix(bg, ink, 0.06).hex(),
		mix(bg, green, 0.12).hex(),
		mix(bg, red, 0.14).hex(),
	)
}

// Suggestion is the accent for a selected autocomplete/dialog row. Kiln
// marks selection with the raised background and an amber key rather than
// a coloured `❯`; this alias keeps existing accent call sites pointing at
// the kiln amber until they move to the raised-row styling in the layout
// pass.
var Suggestion = KilnAmber

// Legacy token names, remapped onto the kiln palette so existing call
// sites recolor without edits. Prefer the kiln helpers above in new code.
var (
	// BrandOrange (logo glyphs, `✻` marker) → kiln amber accent.
	BrandOrange = KilnAmber
	// Muted (secondary/dim text: version, model/effort, cwd, tips, `⎿`
	// rows) → kiln dim.
	Muted = style(lipgloss.NewStyle().Foreground(lipgloss.Color(hexDim)))
	// Amber (mode-line lead-in, `⚠`) → kiln amber.
	Amber = KilnAmber
	// CallGreen (successful tool marker) → kiln green.
	CallGreen = KilnGreen
	// CallRed (failed / disconnected) → kiln red.
	CallRed = KilnRed
)

// RuleColour (block/label hairlines) → kiln rule. A wrapper function
// (rather than a var alias snapshotting Rule once) so it always reflects
// whatever SetTerminalBackground last set Rule to.
func RuleColour(s string) string { return Rule(s) }

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
	// Thinking marks a reasoning block.
	Thinking string
	// UserMark prefixes the user's own messages and the input prompt.
	// kiln uses "›".
	UserMark string
	// Summary marks the line that closes a turn.
	Summary string

	TodoDone    string
	TodoPending string
	TodoActive  string

	Spinner []string

	// kiln block glyphs (Layout 1b "Ruled"). Block labels carry the type;
	// these glyphs mark items and inline status.
	Text        string // assistant/text bullet: "•"
	Note        string // system note: "·"
	Diff        string // diff label glyph: "±"
	Approval    string // permission prompt: "?"
	Plan        string // plan label: "≡"
	Subagents   string // subagents label: "∥"
	Error       string // error label: "!"
	Context     string // context label: "◧"
	OK          string // success: "✓"
	Fail        string // error / declined: "✕"
	PlanCurrent string // plan current item: "▸"
	PlanTodo    string // plan todo item: "○"
	MeterFull   string // progress/meter filled cell: "━"
	MeterEmpty  string // progress/meter empty cell: "─"
	Segment     string // context legend / interrupted marker: "■"
	StreamCaret string // streaming caret: "▍"
	Reconnect   string // reconnect note: "↺"
	Action      string // tool output / subagent action: "→"
}

// UnicodeGlyphs is the default glyph table.
var UnicodeGlyphs = Glyphs{
	Call:        "⏺",
	Result:      "⎿",
	Thinking:    "∴",
	UserMark:    "›",
	Summary:     "✻",
	TodoDone:    "✓",
	TodoPending: "○",
	TodoActive:  "▸",
	// kiln spinner: ◐ ◓ ◑ ◒ at 140ms.
	Spinner:     []string{"◐", "◓", "◑", "◒"},
	Text:        "•",
	Note:        "·",
	Diff:        "±",
	Approval:    "?",
	Plan:        "≡",
	Subagents:   "∥",
	Error:       "!",
	Context:     "◧",
	OK:          "✓",
	Fail:        "✕",
	PlanCurrent: "▸",
	PlanTodo:    "○",
	MeterFull:   "━",
	MeterEmpty:  "─",
	Segment:     "■",
	StreamCaret: "▍",
	Reconnect:   "↺",
	Action:      "→",
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
	Text:        "*",
	Note:        "-",
	Diff:        "~",
	Approval:    "?",
	Plan:        "#",
	Subagents:   "||",
	Error:       "!",
	Context:     "#",
	OK:          "+",
	Fail:        "x",
	PlanCurrent: ">",
	PlanTodo:    "o",
	MeterFull:   "=",
	MeterEmpty:  "-",
	Segment:     "#",
	StreamCaret: "|",
	Reconnect:   "~",
	Action:      "->",
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

// labelRule renders kiln's block header (Layout 1b "Ruled"): a label in
// labelColor, a hairline `─` fill in the rule colour, and right-aligned
// dim meta, fitted to width:
//
//	{label}──────────────────────────────  {meta}
//
// meta may be empty (the fill then runs to the edge). labelColor is one of
// the kiln foreground helpers (KilnAmber, Muted, KilnBlue, KilnRed, ...).
// In plain mode the rule is drawn with ASCII '-' and no colour.
func labelRule(label string, labelColor func(string) string, meta string, width int) string {
	if width <= 0 {
		return label
	}
	fillCh := "─"
	if IsPlain() {
		fillCh = "-"
	}
	// Visible widths of the fixed parts. Layout: label + " " + fill + meta,
	// with two spaces before meta when meta is present.
	lw := VisibleWidth(label)
	rw := 0
	if meta != "" {
		rw = VisibleWidth("  " + meta)
	}
	fillN := width - lw - 1 - rw
	if fillN < 1 {
		fillN = 1
	}
	fill := Rule(strings.Repeat(fillCh, fillN))
	out := labelColor(label) + " " + fill
	if meta != "" {
		out += "  " + Muted(meta)
	}
	return out
}
