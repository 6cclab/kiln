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

// Kiln foreground helpers. Ink, Faint and the named accents default to the
// design's own fixed hexes, but — like the surface tokens below — are
// background-aware: SetTerminalBackground re-derives each of them so body
// text, dim/muted text and every accent stay legible against whatever
// background the terminal actually has, per ensureContrast's thresholds
// (bodyMinContrast, dimMinContrast, accentMinContrast). They are func vars
// (not style() literals assigned once) for the same reason
// Rule/RuleStrong/BarEmpty are: SetTerminalBackground has to be able to
// rebuild them.
var (
	Ink       func(string) string
	Faint     func(string) string
	KilnAmber func(string) string
	KilnGreen func(string) string
	KilnRed   func(string) string
	KilnBlue  func(string) string
	Violet    func(string) string
	// MutedStrike is Muted with a line through it (a done plan item). One
	// style, not Muted(Strike(s)): lipgloss renders strikethrough one cell
	// at a time with a reset after each, which cancels an outer colour
	// after the first character.
	MutedStrike func(string) string
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
	resetTextTokensToDesign()
}

// resetSurfaceTokensToDesign rebuilds every surface token (Rule, RuleStrong,
// BarEmpty, OnRaise, OnPanel, OnDiffAdd, OnDiffDel) from the design's own
// fixed hexes, the state at startup before any SetTerminalBackground call
// and the state SetTerminalBackground restores when the reported background
// is within designBgNearThreshold of hexDesignBg.
func resetSurfaceTokensToDesign() {
	setSurfaceTokens(hexRule, hexRuleStrong, hexBarEmpty, hexRaise, hexPanel, hexDiffAddBg, hexDiffDelBg)
}

// surfaceHex holds the hexes the surface tokens were last built from, so
// tests can check them against the background they sit on.
var surfaceHex struct{ rule, ruleStrong, raise string }

func setSurfaceTokens(rule, ruleStrong, barEmpty, raise, panel, diffAdd, diffDel string) {
	surfaceHex.rule, surfaceHex.ruleStrong, surfaceHex.raise = rule, ruleStrong, raise
	Rule = style(lipgloss.NewStyle().Foreground(lipgloss.Color(rule)))
	RuleStrong = style(lipgloss.NewStyle().Foreground(lipgloss.Color(ruleStrong)))
	BarEmpty = style(lipgloss.NewStyle().Foreground(lipgloss.Color(barEmpty)))
	OnRaise = style(lipgloss.NewStyle().Background(lipgloss.Color(raise)))
	OnPanel = style(lipgloss.NewStyle().Background(lipgloss.Color(panel)))
	OnDiffAdd = style(lipgloss.NewStyle().Background(lipgloss.Color(diffAdd)))
	OnDiffDel = style(lipgloss.NewStyle().Background(lipgloss.Color(diffDel)))
}

// onRaiseSpan renders one span of a raised (OnRaise) row with its own
// foreground baked into the same style call ("" keeps the terminal's
// default foreground for a span that carries no text of its own, e.g. a
// row's trailing fill). A raised row that mixes more than one foreground
// colour — a permission option's amber key next to its ink label, the
// command palette's amber value next to its dim description — cannot be
// built by wrapping OnRaise around content that already went through its
// own independent style call: an ANSI reset (the code every lipgloss
// Render() ends its span with) clears every SGR attribute, not just the
// one that Render() call set, so the raised background dies at the first
// inner reset and every span after it (the gap, the second colour, the
// trailing pad) reverts to the terminal's own background. Composing
// background+foreground in one style per span sidesteps it: each span's
// own reset only ever lands after its own content, and the next span
// re-asserts the background itself. permissionOptionRow, Popup.Render and
// RenderUserMessageMeta all had this bug before this helper (RenderUserMessageMeta's
// case had only one foreground colour, so reordering — pad the plain text,
// then colour it, then raise the whole already-single-span result — was
// enough there instead).
func onRaiseSpan(fg, text string) string {
	if !enabled {
		return text
	}
	st := lipgloss.NewStyle().Background(lipgloss.Color(surfaceHex.raise))
	if fg != "" {
		st = st.Foreground(lipgloss.Color(fg))
	}
	return st.Render(text)
}

// resetTextTokensToDesign rebuilds every text token (Ink, Faint, Muted,
// KilnAmber, KilnGreen, KilnRed, KilnBlue, Violet) from the design's own
// fixed hexes — the state at startup before any SetTerminalBackground call,
// and the state SetTerminalBackground restores when the reported background
// is within designBgNearThreshold of hexDesignBg (existing PTY goldens are
// pinned to these exact hexes on the design background; see theme_test.go).
func resetTextTokensToDesign() {
	setTextTokens(hexInk, hexDim, hexFaint, hexAmber, hexGreen, hexRed, hexBlue, hexViolet)
}

func setTextTokens(ink, dim, faint, amber, green, red, blue, violet string) {
	Ink = style(lipgloss.NewStyle().Foreground(lipgloss.Color(ink)))
	Muted = style(lipgloss.NewStyle().Foreground(lipgloss.Color(dim)))
	MutedStrike = style(lipgloss.NewStyle().Foreground(lipgloss.Color(dim)).Strikethrough(true))
	Faint = style(lipgloss.NewStyle().Foreground(lipgloss.Color(faint)))
	KilnAmber = style(lipgloss.NewStyle().Foreground(lipgloss.Color(amber)))
	KilnGreen = style(lipgloss.NewStyle().Foreground(lipgloss.Color(green)))
	KilnRed = style(lipgloss.NewStyle().Foreground(lipgloss.Color(red)))
	KilnBlue = style(lipgloss.NewStyle().Foreground(lipgloss.Color(blue)))
	Violet = style(lipgloss.NewStyle().Foreground(lipgloss.Color(violet)))
	textHex = TextHex{Ink: ink, Dim: dim, Faint: faint, Amber: amber, Green: green, Red: red, Blue: blue, Violet: violet}
}

// TextHex is the current text tokens' raw hex strings — what setTextTokens
// last set Ink/Muted/Faint/KilnAmber/KilnGreen/KilnRed/KilnBlue/Violet to.
// Consumers that bake a colour into a value they build once and keep across
// repaints (markdown.go's buildStyle feeds these into glamour's
// ansi.StyleConfig, itself rebuilt on every render call) read this instead
// of the package's own hexInk/hexAmber/... consts, which are only ever the
// unadjusted dark-design values.
type TextHex struct {
	Ink, Dim, Faint, Amber, Green, Red, Blue, Violet string
}

var textHex TextHex

// CurrentTextHex returns the active text tokens' hex strings.
func CurrentTextHex() TextHex { return textHex }

// SurfaceHex is the current surface tokens' raw hex strings — what
// setSurfaceTokens last built Rule/RuleStrong/OnRaise from. Consumers that
// bake one of these into a colour they build once and keep across repaints
// (app.go's editor styles: the input box's rules are lipgloss.Style values
// captured at construction, not re-read every frame the way a Rule(...)
// call would be) read this instead of the package's own hexRule/
// hexRuleStrong/... consts, which are only ever the unadjusted dark-design
// values — see CurrentTextHex's own doc comment for the parallel case.
type SurfaceHex struct {
	Rule, RuleStrong, Raise string
}

// CurrentSurfaceHex returns the active surface tokens' hex strings.
func CurrentSurfaceHex() SurfaceHex {
	return SurfaceHex{Rule: surfaceHex.rule, RuleStrong: surfaceHex.ruleStrong, Raise: surfaceHex.raise}
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
// matchDesignSeparation blends bg toward fg until the result stands off bg
// by the same contrast ratio the design's own surface hex has against the
// design background: a hairline stays exactly as faint, and a raised
// surface exactly as raised, on whatever background the terminal has. A
// fixed blend ratio does not do that — luminance is not linear, so the
// same blend reads far weaker on a light background than on a dark one.
func matchDesignSeparation(bg, fg rgb8, designHex string) rgb8 {
	target := contrastRatio(parseHex(hexDesignBg), parseHex(designHex))
	lo, hi := 0.0, 1.0
	for i := 0; i < 24; i++ {
		m := (lo + hi) / 2
		if contrastRatio(bg, mix(bg, fg, m)) < target {
			lo = m
		} else {
			hi = m
		}
	}
	return mix(bg, fg, hi)
}

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
		resetTextTokensToDesign()
		themeGenerationBump()
		return
	}
	green := parseHex(hexGreen)
	red := parseHex(hexRed)
	// Surfaces blend the background toward the legible foreground, not the
	// design's light ink: on a light background the design ink is itself
	// near the background, and blends toward it vanish (hairlines and the
	// raised you-block surface disappeared on a light profile).
	ink := ensureContrast(bg, parseHex(hexInk), bodyMinContrast)
	setSurfaceTokens(
		matchDesignSeparation(bg, ink, hexRule).hex(),
		matchDesignSeparation(bg, ink, hexRuleStrong).hex(),
		matchDesignSeparation(bg, ink, hexBarEmpty).hex(),
		matchDesignSeparation(bg, ink, hexRaise).hex(),
		matchDesignSeparation(bg, ink, hexPanel).hex(),
		mix(bg, green, 0.12).hex(),
		mix(bg, red, 0.14).hex(),
	)
	setTextTokens(
		ink.hex(),
		ensureContrast(bg, parseHex(hexDim), dimMinContrast).hex(),
		ensureContrast(bg, parseHex(hexFaint), dimMinContrast).hex(),
		ensureContrast(bg, parseHex(hexAmber), accentMinContrast).hex(),
		ensureContrast(bg, green, accentMinContrast).hex(),
		ensureContrast(bg, red, accentMinContrast).hex(),
		ensureContrast(bg, parseHex(hexBlue), accentMinContrast).hex(),
		ensureContrast(bg, parseHex(hexViolet), accentMinContrast).hex(),
	)
	themeGenerationBump()
}

// bodyMinContrast is the WCAG 2.1 AAA threshold for normal-size body text
// (Success Criterion 1.4.6): the Ink token (the `you` block's own text)
// must clear this against the detected background. AA's 4.5:1 left the
// user's own text a mid grey on a light profile, visibly fainter than the
// reply body in the terminal's own foreground.
//
// dimMinContrast and accentMinContrast use the AA threshold for large-scale
// text and non-text UI components/graphics (SC 1.4.11, 1.4.3's large-text
// case): dim/muted/faint chrome (labels, meta, statuslines) and every named
// accent (amber/green/red/blue/violet) are either large/bold-weight text,
// single-glyph markers, or decorative, so 3:1 — not 4.5:1 — is the
// applicable bar, and holding accents to 4.5:1 would wash out their hue
// more than legibility requires.
const (
	bodyMinContrast   = 7.0
	dimMinContrast    = 3.0
	accentMinContrast = 3.0
)

// srgbChannel converts one 8-bit sRGB channel to its linear-light value,
// the per-channel step of the WCAG relative luminance formula.
func srgbChannel(v uint8) float64 {
	c := float64(v) / 255
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// relLuminance is the WCAG 2.1 relative luminance of a colour (0-1).
func relLuminance(c rgb8) float64 {
	return 0.2126*srgbChannel(c.r) + 0.7152*srgbChannel(c.g) + 0.0722*srgbChannel(c.b)
}

// contrastRatio is the WCAG 2.1 contrast ratio between two colours
// (1-21, order-independent).
func contrastRatio(a, b rgb8) float64 {
	la, lb := relLuminance(a)+0.05, relLuminance(b)+0.05
	if la < lb {
		la, lb = lb, la
	}
	return la / lb
}

// ensureContrast returns fg unchanged when it already clears minRatio
// against bg. Otherwise it mixes fg toward whichever of pure black/white
// contrasts more against bg (i.e. away from bg's own luminance) in small
// steps, returning the first mix that clears the threshold — the smallest
// nudge off the design's exact hue that gets there — or, if even the
// extreme cannot clear it, the closest mix reached (which is at least the
// most legible option available, that extreme itself).
func ensureContrast(bg, fg rgb8, minRatio float64) rgb8 {
	if contrastRatio(bg, fg) >= minRatio {
		return fg
	}
	black := rgb8{0, 0, 0}
	white := rgb8{0xff, 0xff, 0xff}
	target := black
	if contrastRatio(bg, white) > contrastRatio(bg, black) {
		target = white
	}
	best, bestRatio := fg, contrastRatio(bg, fg)
	for i := 1; i <= 100; i++ {
		t := float64(i) / 100
		candidate := mix(fg, target, t)
		ratio := contrastRatio(bg, candidate)
		if ratio > bestRatio {
			best, bestRatio = candidate, ratio
		}
		if ratio >= minRatio {
			return candidate
		}
	}
	return best
}

// themeGeneration counts every SetTerminalBackground call that changed the
// active tokens (including a reset to the design's own hexes) so callers
// that cache render output by input text alone — MarkdownRenderer's cache
// — can also key on "which token set produced this," and stop serving lines
// coloured before the terminal's background was known. See
// MarkdownRenderer.Render.
var themeGeneration int

func themeGenerationBump() {
	themeGeneration++
}

// ThemeGeneration reports the current token generation; see themeGeneration.
func ThemeGeneration() int {
	return themeGeneration
}

// Suggestion is the accent for a selected autocomplete/dialog row. Kiln
// marks selection with the raised background and an amber key rather than
// a coloured `❯`; this alias keeps existing accent call sites pointing at
// the kiln amber until they move to the raised-row styling in the layout
// pass. A wrapper func (not a var snapshotting KilnAmber once) so it always
// reflects whatever SetTerminalBackground last set KilnAmber to — see
// RuleColour's doc comment for why the surface tokens use the same pattern.
func Suggestion(s string) string { return KilnAmber(s) }

// Legacy token names, remapped onto the kiln palette so existing call
// sites recolor without edits. Prefer the kiln helpers above in new code.
// Wrapper funcs, not var aliases, for the same reason Suggestion is: they
// must track whatever SetTerminalBackground last set the underlying kiln
// token to, not the value it held at package-init time.
var (
	// Muted (secondary/dim text: version, model/effort, cwd, tips, `⎿`
	// rows) → kiln dim.
	Muted func(string) string
)

// BrandOrange (logo glyphs, `✻` marker) → kiln amber accent.
func BrandOrange(s string) string { return KilnAmber(s) }

// Amber (mode-line lead-in, `⚠`) → kiln amber.
func Amber(s string) string { return KilnAmber(s) }

// CallGreen (successful tool marker) → kiln green.
func CallGreen(s string) string { return KilnGreen(s) }

// CallRed (failed / disconnected) → kiln red.
func CallRed(s string) string { return KilnRed(s) }

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

// RuleFillChar is the horizontal rule's fill character: the box-drawing
// "─" normally, ASCII "-" in plain mode (--ax-screen-reader). Every call
// site that draws a rule directly with strings.Repeat — rather than
// through labelRule, which already branches on IsPlain itself — reads this
// instead of hardcoding "─", so plain mode never leaks a box-drawing glyph
// through one of them (defect *screen-reader-mode-leaves-box-drawing-
// rules: permission_render.go's amberRule and the input box's own rule
// were doing exactly that).
func RuleFillChar() string {
	if IsPlain() {
		return "-"
	}
	return "─"
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
	fillCh := RuleFillChar()
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
