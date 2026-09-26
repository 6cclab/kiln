package tui

import (
	"image/color"
	"reflect"
	"testing"

	"charm.land/lipgloss/v2"
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

	wantHex := mix(bg, parseHex(hexInk), 0.16).hex()
	want := style(lipgloss.NewStyle().Foreground(lipgloss.Color(wantHex)))("x")
	if got := Rule("x"); got != want {
		t.Errorf("Rule(x) after bg %+v = %q, want %q (mix(bg, ink, 0.16))", bg, got, want)
	}
}
