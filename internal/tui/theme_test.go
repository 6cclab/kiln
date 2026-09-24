package tui

import (
	"reflect"
	"testing"
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
