package tui

import (
	"image/color"
	"math"
	"reflect"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPlainModeSwapsGlyphsAndColour(t *testing.T) {
	defer func() {
		SetPlainMode(false)
	}()

	SetColorEnabled(true)
	SetGlyphs(UnicodeGlyphs)
	if G().Call != "⏺" {
		t.Fatalf("expected unicode glyphs before plain mode")
	}

	SetPlainMode(true)
	if !IsPlain() {
		t.Error("IsPlain() should report true")
	}
	if IsColorEnabled() {
		t.Error("colour must be disabled in plain mode")
	}
	if !reflect.DeepEqual(G(), ASCIIGlyphs) {
		t.Errorf("glyphs = %+v, want ASCIIGlyphs", G())
	}
	if Bold("x") != "x" {
		t.Errorf("styling must be identity while colour is disabled, got %q", Bold("x"))
	}

	SetPlainMode(false)
	if IsPlain() {
		t.Error("IsPlain() should report false after leaving plain mode")
	}
	if !reflect.DeepEqual(G(), UnicodeGlyphs) {
		t.Error("glyphs should return to unicode after leaving plain mode")
	}
}

func TestStyleIdentityWhenColorDisabled(t *testing.T) {
	prev := enabled
	defer SetColorEnabled(prev)

	SetColorEnabled(false)
	for name, fn := range map[string]func(string) string{
		"Dim": Dim, "Bold": Bold, "Italic": Italic, "Strike": Strike,
		"Red": Red, "Green": Green, "Yellow": Yellow, "Blue": Blue,
		"Magenta": Magenta, "Cyan": Cyan, "Gray": Gray, "Suggestion": Suggestion,
	} {
		if got := fn("plain"); got != "plain" {
			t.Errorf("%s with colour disabled = %q, want identity", name, got)
		}
	}

	SetColorEnabled(true)
	for name, fn := range map[string]func(string) string{
		"Bold": Bold, "Red": Red, "Suggestion": Suggestion,
	} {
		if got := fn("styled"); got == "styled" {
			t.Errorf("%s with colour enabled should not be identity", name)
		}
	}
}

// --- SetTerminalBackground / blend math ------------------------------------

func TestMixBlend(t *testing.T) {
	bg := rgb8{0x10, 0x20, 0x30}
	fg := rgb8{0xf0, 0xe0, 0xd0}

	if got := mix(bg, fg, 0); got != bg {
		t.Errorf("mix(bg, fg, 0) = %+v, want bg %+v", got, bg)
	}
	if got := mix(bg, fg, 1); got != fg {
		t.Errorf("mix(bg, fg, 1) = %+v, want fg %+v", got, fg)
	}
	// Halfway should land exactly between each channel.
	want := rgb8{
		r: uint8((int(bg.r) + int(fg.r)) / 2),
		g: uint8((int(bg.g) + int(fg.g)) / 2),
		b: uint8((int(bg.b) + int(fg.b)) / 2),
	}
	if got := mix(bg, fg, 0.5); got != want {
		t.Errorf("mix(bg, fg, 0.5) = %+v, want %+v", got, want)
	}
}

func TestParseHexAndToRGB8Roundtrip(t *testing.T) {
	c := parseHex(hexDesignBg)
	if got, want := c.hex(), hexDesignBg; got != want {
		t.Errorf("parseHex(%q).hex() = %q, want %q", hexDesignBg, got, want)
	}

	// tea.BackgroundColorMsg carries an arbitrary image/color.Color, often
	// at 16 bits/channel (an OSC 11 reply); toRGB8 must downsample it to the
	// same 8-bit value parseHex would have produced from the equivalent hex
	// string, so a real terminal's reply and this package's own hex consts
	// compare on equal footing.
	rgba := color.RGBA{R: 0x14, G: 0x11, B: 0x0d, A: 0xff}
	if got, want := toRGB8(rgba).hex(), hexDesignBg; got != want {
		t.Errorf("toRGB8(%+v).hex() = %q, want %q", rgba, got, want)
	}
}

func TestSetTerminalBackground_NearDesignKeepsExactHexes(t *testing.T) {
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		resetSurfaceTokensToDesign()
	})
	SetColorEnabled(true)
	resetSurfaceTokensToDesign()
	want := Rule("x")

	// The design background itself, and a background merely close to it
	// (within designBgNearThreshold), must both keep the design's exact
	// hexes rather than recomputing a near-identical blend.
	SetTerminalBackground(color.RGBA{R: 0x14, G: 0x11, B: 0x0d, A: 0xff})
	if got := Rule("x"); got != want {
		t.Errorf("Rule(x) after the exact design bg = %q, want %q (unchanged)", got, want)
	}

	SetTerminalBackground(color.RGBA{R: 0x16, G: 0x13, B: 0x0f, A: 0xff})
	if got := Rule("x"); got != want {
		t.Errorf("Rule(x) after a bg close to design = %q, want %q (unchanged)", got, want)
	}
}

// --- text token contrast (light-bg-you-text-invisible fix) -----------------

// TestContrastRatioKnownValues pins contrastRatio's WCAG formula against a
// couple of textbook values: pure black vs. pure white is the maximum
// 21:1, and a colour against itself is the minimum 1:1.
func TestContrastRatioKnownValues(t *testing.T) {
	black := rgb8{0, 0, 0}
	white := rgb8{0xff, 0xff, 0xff}
	if got := contrastRatio(black, white); math.Abs(got-21) > 0.01 {
		t.Errorf("contrastRatio(black, white) = %.4f, want 21", got)
	}
	if got := contrastRatio(white, white); math.Abs(got-1) > 0.0001 {
		t.Errorf("contrastRatio(white, white) = %.4f, want 1", got)
	}
}

// TestEnsureContrastNoOpWhenAlreadyLegible confirms ensureContrast leaves a
// colour untouched when it already clears the threshold — the design's own
// dark background must never see its exact hexes perturbed.
func TestEnsureContrastNoOpWhenAlreadyLegible(t *testing.T) {
	bg := rgb8{0x14, 0x11, 0x0d} // hexDesignBg
	ink := parseHex(hexInk)      // near-white on near-black: already >4.5:1
	if got := ensureContrast(bg, ink, bodyMinContrast); got != ink {
		t.Errorf("ensureContrast(design bg, ink, 4.5) = %+v, want unchanged %+v", got, ink)
	}
}

// TestEnsureContrastDarkensForLightBackground is the direct regression for
// qa/findings/20260926T231105Z-light-bg-you-text-invisible.json: hexInk
// (near-white body text) against the QA light-profile background
// (#f7f4ee, scripts/qa/drive.py's ensure_light_profile) starts at ~1.15:1 —
// invisible — and ensureContrast must bring it to at least 4.5:1.
func TestEnsureContrastDarkensForLightBackground(t *testing.T) {
	bg := parseHex("#f7f4ee")
	ink := parseHex(hexInk)

	before := contrastRatio(bg, ink)
	if before >= bodyMinContrast {
		t.Fatalf("test assumption broken: hexInk already clears %.1f:1 against the light bg (%.2f:1) — pick a token that actually fails", bodyMinContrast, before)
	}

	after := ensureContrast(bg, ink, bodyMinContrast)
	ratio := contrastRatio(bg, after)
	if ratio < bodyMinContrast {
		t.Errorf("ensureContrast(lightBg, ink, 4.5) = %+v (%.2f:1), want >= 4.5:1", after, ratio)
	}
}

// TestSetTerminalBackground_LightBackgroundTextTokensMeetContrast is the
// end-to-end regression: every text token SetTerminalBackground recomputes
// for the QA light profile's background must clear its threshold —
// 7:1 for Ink, 4.5:1 (small text) for Muted and the named accents, which
// colour normal-size words such as the mode label, and 3:1 for Faint. The
// bars are literals so lowering a floor in theme.go fails here.
func TestSetTerminalBackground_LightBackgroundTextTokensMeetContrast(t *testing.T) {
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		resetSurfaceTokensToDesign()
		resetTextTokensToDesign()
	})
	SetColorEnabled(true)
	resetTextTokensToDesign()

	SetTerminalBackground(color.RGBA{R: 0xf7, G: 0xf4, B: 0xee, A: 0xff})
	bg := parseHex("#f7f4ee")

	tx := CurrentTextHex()
	cases := []struct {
		name string
		hex  string
		min  float64
	}{
		{"Ink", tx.Ink, 7.0},
		{"Muted/Dim", tx.Dim, 4.5},
		{"Faint", tx.Faint, 3.0},
		{"KilnAmber", tx.Amber, 4.5},
		{"KilnGreen", tx.Green, 4.5},
		{"KilnRed", tx.Red, 4.5},
		{"KilnBlue", tx.Blue, 4.5},
		{"Violet", tx.Violet, 4.5},
	}
	for _, c := range cases {
		ratio := contrastRatio(bg, parseHex(c.hex))
		if ratio < c.min {
			t.Errorf("%s = %s against light bg #f7f4ee: contrast %.2f:1, want >= %.1f:1", c.name, c.hex, ratio, c.min)
		}
	}
}

// TestSetTerminalBackground_NearDesignKeepsExactTextHexes is resetTextTokensToDesign's
// half of TestSetTerminalBackground_NearDesignKeepsExactHexes: a background
// at (or within designBgNearThreshold of) the design's own must leave the
// text tokens at the design's exact hexes, matching the PTY goldens pinned
// to them.
func TestSetTerminalBackground_NearDesignKeepsExactTextHexes(t *testing.T) {
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		resetTextTokensToDesign()
	})
	SetColorEnabled(true)
	resetTextTokensToDesign()

	SetTerminalBackground(color.RGBA{R: 0x14, G: 0x11, B: 0x0d, A: 0xff})
	tx := CurrentTextHex()
	if tx.Ink != hexInk || tx.Amber != hexAmber || tx.Dim != hexDim {
		t.Errorf("text tokens after the exact design bg = %+v, want the design's exact hexes (ink=%s amber=%s dim=%s)", tx, hexInk, hexAmber, hexDim)
	}
}

func TestSetTerminalBackground_FarRecomputesSurfaceTokens(t *testing.T) {
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		resetSurfaceTokensToDesign()
	})
	SetColorEnabled(true)
	resetSurfaceTokensToDesign()
	designRule := Rule("x")

	// A background nothing like the design's near-black (light grey, the
	// kind of "gray-black" real-terminal background this bug report was
	// about) must recompute the surface tokens rather than keep them.
	bg := rgb8{0x40, 0x40, 0x40}
	SetTerminalBackground(color.RGBA{R: bg.r, G: bg.g, B: bg.b, A: 0xff})
	if got := Rule("x"); got == designRule {
		t.Errorf("Rule(x) after a far bg == the design's own Rule(x); want it recomputed")
	}

	ink := ensureContrast(bg, parseHex(hexInk), bodyMinContrast)
	wantHex := matchDesignSeparation(bg, ink, hexRule).hex()
	want := style(lipgloss.NewStyle().Foreground(lipgloss.Color(wantHex)))("x")
	if got := Rule("x"); got != want {
		t.Errorf("Rule(x) after bg %+v = %q, want %q (design separation from bg)", bg, got, want)
	}
}

// TestSetTerminalBackground_LightBackgroundSurfacesVisible: on a light
// profile the label hairlines and the raised you-block surface stand off
// the background by the same contrast their design hexes have on the
// design background. They used to blend toward the design's light ink,
// which is itself near a light background, and vanished.
func TestSetTerminalBackground_LightBackgroundSurfacesVisible(t *testing.T) {
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		resetSurfaceTokensToDesign()
		resetTextTokensToDesign()
	})
	SetColorEnabled(true)
	bg := parseHex("#f7f4ee") // scripts/qa/drive.py's light QA profile
	SetTerminalBackground(color.RGBA{R: bg.r, G: bg.g, B: bg.b, A: 0xff})
	for name, c := range map[string]struct {
		hex string
		min float64
	}{
		"rule":       {surfaceHex.rule, designSeparation(hexRule)},
		"ruleStrong": {surfaceHex.ruleStrong, designSeparation(hexRuleStrong)},
		"raise":      {surfaceHex.raise, designSeparation(hexRaise)},
	} {
		if got := contrastRatio(bg, parseHex(c.hex)); got < c.min-0.01 {
			t.Errorf("%s %s on %s: contrast %.2f, want >= %.2f", name, c.hex, bg.hex(), got, c.min)
		}
	}
}

func designSeparation(hex string) float64 {
	return contrastRatio(parseHex(hexDesignBg), parseHex(hex))
}

// TestSetTerminalBackground_LightSurfacesStayVisible: hairlines, raised
// rows, empty meter cells and diff tints keep the design's perceived step
// off the background on a light profile (a review measured raised rows at
// 1.18:1 and diff rows at 1.07:1 when separation matched the design's
// contrast ratio, or used a fixed blend), and dim text stays a step above
// faint.
func TestSetTerminalBackground_LightSurfacesStayVisible(t *testing.T) {
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		resetSurfaceTokensToDesign()
		resetTextTokensToDesign()
	})
	SetColorEnabled(true)
	bg := parseHex("#f5f3ec")
	SetTerminalBackground(color.RGBA{R: 0xf5, G: 0xf3, B: 0xec, A: 0xff})
	design := parseHex(hexDesignBg)
	sf := CurrentSurfaceHex()
	for _, c := range []struct{ name, got, designHex string }{
		{"Rule", sf.Rule, hexRule},
		{"Raise", sf.Raise, hexRaise},
		{"BarEmpty", sf.BarEmpty, hexBarEmpty},
		{"DiffAdd", sf.DiffAdd, hexDiffAddBg},
		{"DiffDel", sf.DiffDel, hexDiffDelBg},
	} {
		want := math.Abs(lightness(parseHex(c.designHex)) - lightness(design))
		got := math.Abs(lightness(parseHex(c.got)) - lightness(bg))
		if got+0.5 < want {
			t.Errorf("%s = %s: lightness step %.1f off the background, want the design's %.1f", c.name, c.got, got, want)
		}
	}
	tx := CurrentTextHex()
	if contrastRatio(bg, parseHex(tx.Dim)) <= contrastRatio(bg, parseHex(tx.Faint)) {
		t.Errorf("dim %s does not stand above faint %s", tx.Dim, tx.Faint)
	}
}

// TestColorProfile256_SurfacesSurviveThePalette: a 256-colour terminal
// (Terminal.app) gets every colour through ansi.Convert256, which picks the
// nearest palette entry; for surfaces meant to sit just off the background
// that was often the background's own grey, and diff tints and the empty
// context bar vanished. With the 256 profile set, each surface as rendered
// (after Convert256) keeps most of the design's lightness step.
func TestColorProfile256_SurfacesSurviveThePalette(t *testing.T) {
	// Added and removed rows must also look different from each other, not
	// only from the background.
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		SetColorProfile256(false)
		resetSurfaceTokensToDesign()
		resetTextTokensToDesign()
	})
	SetColorEnabled(true)
	design := parseHex(hexDesignBg)
	for _, bgHex := range []string{hexDesignBg, "#1e1e1e", "#f5f3ec"} {
		bg := parseHex(bgHex)
		SetColorProfile256(true)
		SetTerminalBackground(color.RGBA{R: bg.r, G: bg.g, B: bg.b, A: 0xff})
		sf := CurrentSurfaceHex()
		if ansi.Convert256(lipgloss.Color(sf.DiffAdd)) == ansi.Convert256(lipgloss.Color(sf.DiffDel)) {
			t.Errorf("bg %s: added and removed rows render alike (%s, %s)", bgHex, sf.DiffAdd, sf.DiffDel)
		}
		for _, c := range []struct{ name, got, designHex string }{
			{"Rule", sf.Rule, hexRule},
			{"Raise", sf.Raise, hexRaise},
			{"BarEmpty", sf.BarEmpty, hexBarEmpty},
			{"DiffAdd", sf.DiffAdd, hexDiffAddBg},
			{"DiffDel", sf.DiffDel, hexDiffDelBg},
		} {
			rendered := xterm256RGB(int(ansi.Convert256(lipgloss.Color(c.got))))
			want := math.Abs(lightness(parseHex(c.designHex)) - lightness(design))
			got := math.Abs(lightness(rendered) - lightness(bg))
			floor := 0.9
			if c.name == "DiffAdd" || c.name == "DiffDel" {
				floor = 0.8 // tints also separate by hue
			}
			if got < floor*want {
				t.Errorf("bg %s: %s = %s renders as %s, lightness step %.1f, want at least %.0f%%%% of the design's %.1f",
					bgHex, c.name, c.got, rendered.hex(), got, floor*100, want)
			}
		}
	}
}

// TestSetTerminalBackground_NearDesignDarkKeepsSteps: Terminal.app's and
// iTerm2's default dark backgrounds (#1c1c1c, #1e1e1e) are close to the
// design's #14110d but a few lightness units lighter; the design's own
// hexes then sit too near them (diff tints drew at under half the
// design's step). Only a background that is essentially the design's
// keeps the design's hexes.
func TestSetTerminalBackground_NearDesignDarkKeepsSteps(t *testing.T) {
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		resetSurfaceTokensToDesign()
		resetTextTokensToDesign()
	})
	SetColorEnabled(true)
	design := parseHex(hexDesignBg)
	for _, bgHex := range []string{"#1c1c1c", "#1e1e1e"} {
		bg := parseHex(bgHex)
		SetTerminalBackground(color.RGBA{R: bg.r, G: bg.g, B: bg.b, A: 0xff})
		sf := CurrentSurfaceHex()
		for _, c := range []struct{ name, got, designHex string }{
			{"Rule", sf.Rule, hexRule},
			{"Raise", sf.Raise, hexRaise},
			{"BarEmpty", sf.BarEmpty, hexBarEmpty},
			{"DiffAdd", sf.DiffAdd, hexDiffAddBg},
			{"DiffDel", sf.DiffDel, hexDiffDelBg},
		} {
			want := math.Abs(lightness(parseHex(c.designHex)) - lightness(design))
			got := math.Abs(lightness(parseHex(c.got)) - lightness(bg))
			if got+0.5 < want {
				t.Errorf("bg %s: %s = %s, lightness step %.1f, want the design's %.1f", bgHex, c.name, c.got, got, want)
			}
		}
	}
	SetTerminalBackground(color.RGBA{R: design.r, G: design.g, B: design.b, A: 0xff})
	if got := CurrentSurfaceHex().DiffAdd; got != hexDiffAddBg {
		t.Errorf("on the design's own background DiffAdd = %s, want the design's %s", got, hexDiffAddBg)
	}
}
