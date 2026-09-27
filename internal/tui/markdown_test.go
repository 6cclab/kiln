package tui

import (
	"fmt"
	"image/color"
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

// TestMarkdownCacheInvalidatesOnThemeChange regresses the cache half of
// qa/findings/20260926T231105Z-light-bg-you-text-invisible.json: the
// finding calls out that MarkdownRenderer's cache is keyed only by text, so
// a block rendered (and cached) before SetTerminalBackground detected a
// light terminal kept the pre-detection dark-design colours baked into its
// ANSI escapes for the rest of the session. `code` renders in KilnAmber
// (colorYellow in buildStyle), one of the tokens SetTerminalBackground
// recomputes for a light background, so its rendered escape sequence must
// change once the background is (re)detected as light, even though the
// same renderer instance and the same input text are reused.
func TestMarkdownCacheInvalidatesOnThemeChange(t *testing.T) {
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		resetSurfaceTokensToDesign()
		resetTextTokensToDesign()
	})
	SetColorEnabled(true)
	resetSurfaceTokensToDesign()
	resetTextTokensToDesign()

	r := NewMarkdownRenderer(80, false)
	const text = "`code`"

	beforeLines := r.Render(text)
	before := strings.Join(beforeLines, "\n")

	SetTerminalBackground(color.RGBA{R: 0xf7, G: 0xf4, B: 0xee, A: 0xff})

	afterLines := r.Render(text)
	after := strings.Join(afterLines, "\n")

	if before == after {
		t.Fatalf("Render(%q) unchanged after SetTerminalBackground recoloured KilnAmber; cache served stale pre-detection colours:\nbefore=%q\nafter=%q", text, before, after)
	}

	// The freshly rendered escape sequence must carry the *new* amber
	// colour's RGB triplet (lipgloss emits truecolor ANSI as decimal
	// "38;2;R;G;B", not the hex string), not the design's original amber
	// still baked into the cached entry.
	newAmberHex := CurrentTextHex().Amber
	if newAmberHex == hexAmber {
		t.Fatal("test assumption broken: KilnAmber unchanged by SetTerminalBackground for this background")
	}
	rgb := parseHex(newAmberHex)
	wantSeq := fmt.Sprintf("38;2;%d;%d;%d", rgb.r, rgb.g, rgb.b)
	if !strings.Contains(after, wantSeq) {
		t.Errorf("Render(%q) after SetTerminalBackground = %q, want it to contain the recomputed amber ANSI sequence %q (hex %s)", text, after, wantSeq, newAmberHex)
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

const qaTable = "| Route | Limit |\n|---|---|\n| `/api/upload` | 10/min |\n| /api/login | 5/min |"

// TestMarkdownTableSizesToContent: glamour spreads a table across the whole
// word-wrap width; kiln shrinks it to its content (2-column table at 120
// columns stayed ~120 wide with "Limit" at column ~60).
func TestMarkdownTableSizesToContent(t *testing.T) {
	lines := NewMarkdownRenderer(120, false).Render(qaTable)
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 30 {
			t.Errorf("table row %q is %d columns wide, want <= 30", ansi.Strip(l), w)
		}
	}
	if i := strings.Index(ansi.Strip(lines[0]), "Limit"); i < 0 || i > 20 {
		t.Errorf("header %q: Limit at %d, want within 20 columns", ansi.Strip(lines[0]), i)
	}
	for _, want := range []string{"/api/upload", "10/min", "/api/login", "5/min"} {
		if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), want) {
			t.Errorf("cell %q lost:\n%s", want, ansi.Strip(strings.Join(lines, "\n")))
		}
	}
}

// TestMarkdownTableKeepsInlineCellStyling: cells stay glamour-rendered, so
// inline code shows as code, not as literal backticks.
func TestMarkdownTableKeepsInlineCellStyling(t *testing.T) {
	out := ansi.Strip(strings.Join(NewMarkdownRenderer(120, false).Render(qaTable), "\n"))
	if strings.Contains(out, "`") {
		t.Errorf("inline code rendered with literal backticks:\n%s", out)
	}
}

// TestMarkdownTableRulesUseRuleColour: the header rule and column dividers
// draw in the hairline Rule token, not the terminal's default foreground.
func TestMarkdownTableRulesUseRuleColour(t *testing.T) {
	prev := enabled
	t.Cleanup(func() { SetColorEnabled(prev) })
	SetColorEnabled(true)
	lines := NewMarkdownRenderer(120, false).Render(qaTable)
	joined := strings.Join(lines, "\n")
	for _, glyph := range []string{"│", "┼"} {
		if !strings.Contains(joined, Rule(glyph)) {
			t.Errorf("%s is not drawn in the Rule colour:\n%q", glyph, joined)
		}
	}
}

// TestMarkdownTablePlainModeUsesASCIIRules covers defect
// *screen-reader-mode-leaves-box-drawing-rules' one gap in buildStyle's own
// plain branch: glamour has no Table entry in ansi.StyleConfig to override,
// so its raw table output always uses box-drawing characters regardless of
// plain mode — compactTable used to pass tableColSep/tableRuleRun/
// tableCross straight through unchanged (only dropping their colour, via
// its own colour() closure) rather than swapping in the ASCII replacements
// the way every other rule in the package does.
func TestMarkdownTablePlainModeUsesASCIIRules(t *testing.T) {
	lines := NewMarkdownRenderer(120, true).Render(qaTable)
	joined := strings.Join(lines, "\n")
	for _, glyph := range []string{"│", "─", "┼"} {
		if strings.Contains(joined, glyph) {
			t.Errorf("plain-mode table still contains box-drawing glyph %q:\n%s", glyph, joined)
		}
	}
	for _, want := range []string{"|", "-", "+", "/api/upload", "10/min"} {
		if !strings.Contains(joined, want) {
			t.Errorf("plain-mode table missing %q:\n%s", want, joined)
		}
	}
}

// TestMarkdownNoLeadingBlankRow: whatever block a reply opens with, its
// first rendered row carries content, so the body sits right under the
// block label (a blockquote or table used to open with an empty row).
func TestMarkdownNoLeadingBlankRow(t *testing.T) {
	for name, text := range map[string]string{
		"blockquote": "> A 429 response should carry Retry-After.\n\nThat's the gap.",
		"table":      qaTable,
		"list":       "- one\n- two",
		"code":       "```\nx := 1\n```",
		"paragraph":  "Plain text.",
	} {
		lines := NewMarkdownRenderer(80, false).Render(text)
		if len(lines) == 0 || strings.TrimSpace(ansi.Strip(lines[0])) == "" {
			t.Errorf("%s: first row is blank: %q", name, lines)
		}
		if strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
			t.Errorf("%s: last row is blank: %q", name, lines)
		}
	}
}
