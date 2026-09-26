package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownRendersHeadingAndBold(t *testing.T) {
	r := NewMarkdownRenderer(80, true) // plain: assert on stable text, not ANSI codes
	out := r.Render("# Title\n\n**bold** and *italic*")
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "Title") {
		t.Errorf("heading text missing: %q", joined)
	}
	if !strings.Contains(joined, "bold") || !strings.Contains(joined, "italic") {
		t.Errorf("body text missing: %q", joined)
	}
}

func TestMarkdownFitsWidth(t *testing.T) {
	r := NewMarkdownRenderer(width40, true)
	out := r.Render(strings.Repeat("word ", 80))
	for i, l := range out {
		if VisibleWidth(l) > width40 {
			t.Errorf("line %d overflowed width: %q", i, l)
		}
	}
}

func TestMarkdownCachesByTextAndWidth(t *testing.T) {
	r := NewMarkdownRenderer(80, true)
	first := r.Render("hello world")
	second := r.Render("hello world")
	if len(first) != len(second) {
		t.Error("cached render should be stable")
	}
	other := NewMarkdownRenderer(20, true)
	diff := other.Render(strings.Repeat("hello world ", 5))
	if len(diff) == 0 {
		t.Error("a different renderer/width should still render")
	}
}

func TestMarkdownFencedCodeBlock(t *testing.T) {
	r := NewMarkdownRenderer(80, false)
	out := r.Render("```go\nfunc main() {}\n```")
	if len(out) == 0 {
		t.Fatal("expected rendered lines for a fenced code block")
	}
	// Chroma highlights "func" and "main" as separate coloured tokens, so
	// the substring only survives once the SGR sequences between them are
	// stripped.
	stripped := ansi.Strip(strings.Join(out, "\n"))
	if !strings.Contains(stripped, "func main() {}") {
		t.Errorf("code content missing: %v", out)
	}
}

// Regression for the ordered-list marker bug: glamour's ItemElement.Render
// (ansi/listitem.go) writes the numeral as its own unstyled Prefix and, with
// no Enumeration style configured, nothing separates it from the item text
// — "1Point me at the real repo." buildStyle now sets Enumeration.Suffix so
// the marker reads "1. " like the design's bullet items.
func TestMarkdownOrderedListHasSeparator(t *testing.T) {
	r := NewMarkdownRenderer(80, true) // plain: assert on stable text, not ANSI codes
	out := r.Render("1. Point me at the real repo.\n2. Second item.")
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "1Point") || strings.Contains(joined, "2Second") {
		t.Errorf("ordered list marker glued to text: %q", joined)
	}
	if !strings.Contains(joined, "1. Point me at the real repo.") {
		t.Errorf("expected numbered marker with separator: %q", joined)
	}
	if !strings.Contains(joined, "2. Second item.") {
		t.Errorf("expected second numbered marker with separator: %q", joined)
	}
}

// glamour pads every rendered line to the word-wrap width (a code block's
// background/margin box, even unstyled); pi-tui's Markdown component never
// did this, only wrapping, so Render trims that trailing padding to match.
func TestMarkdownTrimsTrailingPadding(t *testing.T) {
	r := NewMarkdownRenderer(80, false)
	out := r.Render("```go\nfunc main() {}\n```")
	for i, l := range out {
		if strings.HasSuffix(ansi.Strip(l), " ") {
			t.Errorf("line %d has trailing padding: %q", i, l)
		}
	}
}
