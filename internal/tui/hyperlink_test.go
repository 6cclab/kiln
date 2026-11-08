package tui

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/mods"
	"github.com/charmbracelet/x/ansi"
)

// withEnabled runs fn with the global styling switch set to v and restores it.
func withEnabled(t *testing.T, v bool, fn func()) {
	t.Helper()
	prev := enabled
	enabled = v
	defer func() { enabled = prev }()
	fn()
}

func TestHyperlink(t *testing.T) {
	const url = "https://example.com/docs"
	withEnabled(t, true, func() {
		got := Hyperlink(url, "docs")
		want := ansi.SetHyperlink(url) + "docs" + ansi.ResetHyperlink()
		if got != want {
			t.Fatalf("Hyperlink = %q, want %q", got, want)
		}
		// The escape must be zero-width so it never disturbs layout.
		if w := VisibleWidth(got); w != 4 {
			t.Errorf("VisibleWidth(link) = %d, want 4", w)
		}
		// Visible text is exactly the label.
		if vis := ansi.Strip(got); vis != "docs" {
			t.Errorf("visible = %q, want %q", vis, "docs")
		}
	})
}

func TestHyperlinkNoops(t *testing.T) {
	withEnabled(t, true, func() {
		if got := Hyperlink("", "docs"); got != "docs" {
			t.Errorf("empty url: got %q, want %q", got, "docs")
		}
	})
	withEnabled(t, false, func() {
		if got := Hyperlink("https://example.com", "docs"); got != "docs" {
			t.Errorf("disabled: got %q, want label unchanged", got)
		}
	})
}

// TestRenderLinkHyperlink is the regression for the mod <a href> gap: a link
// element must render its label clickable (OSC 8) to href, with the visible
// text unchanged and no width inflation from the escape.
func TestRenderLinkHyperlink(t *testing.T) {
	withEnabled(t, true, func() {
		el := mods.Element{
			Props: map[string]any{
				"label": "the docs",
				"href":  "https://example.com/guide",
			},
		}
		box := renderLink(el, 40)
		line := box.lines[0]
		if !strings.Contains(line, ansi.SetHyperlink("https://example.com/guide")) {
			t.Errorf("href not wired as OSC 8 hyperlink: %q", line)
		}
		if vis := ansi.Strip(line); vis != "the docs" {
			t.Errorf("visible = %q, want %q", vis, "the docs")
		}
		if box.width != 8 {
			t.Errorf("box.width = %d, want 8 (escape must be zero-width)", box.width)
		}
	})
}

// TestRenderLinkNoHref: a link without href renders the label with no OSC 8.
func TestRenderLinkNoHref(t *testing.T) {
	withEnabled(t, true, func() {
		el := mods.Element{Props: map[string]any{"label": "plain"}}
		line := renderLink(el, 40).lines[0]
		if strings.Contains(line, "\x1b]8") {
			t.Errorf("no href should emit no OSC 8: %q", line)
		}
		if vis := ansi.Strip(line); vis != "plain" {
			t.Errorf("visible = %q, want %q", vis, "plain")
		}
	})
}
