package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/andrepato/harness/internal/commands"
	"github.com/charmbracelet/x/ansi"
)

// funcPointer returns f's entry point, for comparing that two
// func(string) string values are literally the same package-level style
// (lipgloss disables color in a non-tty test process, so comparing
// RENDERED strings would pass for any two styles — this compares
// identity instead).
func funcPointer(f func(string) string) uintptr {
	return reflect.ValueOf(f).Pointer()
}

// TestRenderContextBar_GapsBetweenSegments covers finding 4's bar half:
// docs/kiln-design-handoff/README.md's stacked bar has a genuine 1-column
// gap between adjacent segments, not one unbroken run of filled cells.
func TestRenderContextBar_GapsBetweenSegments(t *testing.T) {
	b := commands.ContextBreakdown{
		Window: 100,
		Segments: []commands.ContextSegment{
			{Label: "System prompt", Tokens: 25},
			{Label: "Tools", Tokens: 25},
			{Label: "Conversation", Tokens: 25},
			{Label: "Free", Tokens: 25},
		},
	}
	plain := ansi.Strip(renderContextBar(b, 80))
	if strings.Contains(plain, "  ") {
		t.Errorf("bar has a double space, want exactly one gap column between segments: %q", plain)
	}
	gaps := strings.Count(plain, " ")
	if gaps != 3 {
		t.Errorf("bar has %d gap columns, want 3 (one between each of the 4 segments): %q", gaps, plain)
	}
	if n := utf8.RuneCountInString(plain); n != 80 {
		t.Errorf("bar plain-text width = %d runes, want exactly 80", n)
	}
}

// TestRenderContextBar_SkipsZeroSegments covers a zero-token segment
// (e.g. Conversation clamped to 0) never getting its own gap or cell —
// only segments that are actually present in the legend get a run in the
// bar.
func TestRenderContextBar_SkipsZeroSegments(t *testing.T) {
	b := commands.ContextBreakdown{
		Window: 100,
		Segments: []commands.ContextSegment{
			{Label: "System prompt", Tokens: 50},
			{Label: "Tools", Tokens: 0},
			{Label: "Conversation", Tokens: 0},
			{Label: "Free", Tokens: 50},
		},
	}
	plain := ansi.Strip(renderContextBar(b, 80))
	if gaps := strings.Count(plain, " "); gaps != 1 {
		t.Errorf("bar has %d gap columns, want exactly 1 (only 2 non-zero segments): %q", gaps, plain)
	}
}

// TestContextSegmentColor_FilesReadIsAmber covers defect 2's colour half:
// docs/kiln-design-handoff/Terminal.dc.html line 227 specifies "Files
// read" as amber (C.amber); before this fix, contextSegmentColor had no
// case for that label, so it fell through to the "Free" default (Rule).
func TestContextSegmentColor_FilesReadIsAmber(t *testing.T) {
	got := funcPointer(contextSegmentColor("Files read"))
	if want := funcPointer(KilnAmber); got != want {
		t.Errorf("contextSegmentColor(%q) is not KilnAmber", "Files read")
	}
	// Sanity: it must not be the "Free"-segment fallback colour.
	if free := funcPointer(Rule); got == free {
		t.Errorf("contextSegmentColor(%q) resolves to the Free/default colour (Rule), want it distinct", "Files read")
	}
}

// TestRenderContext_FilesReadSegmentRenders covers the full five-segment
// legend (System prompt, Tools, Files read, Conversation, Free) drawing
// its own coloured swatch and row rather than being silently absorbed.
func TestRenderContext_FilesReadSegmentRenders(t *testing.T) {
	b := commands.ContextBreakdown{
		ModelLabel: "kiln-large",
		Used:       76_000,
		Window:     200_000,
		Segments: []commands.ContextSegment{
			{Label: "System prompt", Tokens: 8_000},
			{Label: "Tools", Tokens: 32_000},
			{Label: "Files read", Tokens: 20_000},
			{Label: "Conversation", Tokens: 16_000},
			{Label: "Free", Tokens: 124_000},
		},
	}
	lines := RenderContext(b, 80)
	joined := strings.Join(lines, "\n")
	plain := ansi.Strip(joined)
	if !strings.Contains(plain, "Files read") {
		t.Fatalf("RenderContext output has no \"Files read\" row:\n%s", plain)
	}
	if gaps := strings.Count(ansi.Strip(renderContextBar(b, 80)), " "); gaps != 4 {
		t.Errorf("bar has %d gap columns, want 4 (one between each of the 5 non-zero segments)", gaps)
	}
}

// TestRenderContextBar_EachNonEmptySegmentAtLeastOneCell covers a segment
// too small to earn a proportional cell (e.g. 1 of 128000 tokens at width
// 40) still rendering at least one cell, so it is never silently invisible.
func TestRenderContextBar_EachNonEmptySegmentAtLeastOneCell(t *testing.T) {
	b := commands.ContextBreakdown{
		Window: 128_000,
		Segments: []commands.ContextSegment{
			{Label: "System prompt", Tokens: 1},
			{Label: "Tools", Tokens: 1},
			{Label: "Conversation", Tokens: 1},
			{Label: "Free", Tokens: 127_997},
		},
	}
	plain := ansi.Strip(renderContextBar(b, 40))
	fills := strings.ReplaceAll(plain, " ", "")
	if len([]rune(fills)) < 4 {
		t.Errorf("expected at least 1 cell per non-zero segment (4 segments), got %q", plain)
	}
}
