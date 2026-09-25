package editor

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func testStyles() Styles {
	return Styles{
		Marker:      "❯",
		Rule:        lipgloss.NewStyle(),
		Placeholder: lipgloss.NewStyle(),
	}
}

// widthsUnderTest mirrors tui-render.test.ts's regression widths.
var widthsUnderTest = []int{20, 40, 80, 117, 118, 144}

var contentsUnderTest = []string{
	"",
	"hi",
	"fix typecheck errors",
	strings.Repeat("x", 400),
}

func TestViewWidthInvariant(t *testing.T) {
	for _, width := range widthsUnderTest {
		for _, content := range contentsUnderTest {
			m := New(testStyles())
			m.Focus()
			m.SetValue(content)
			for _, line := range m.View(width) {
				if got := ansi.StringWidth(line); got != width {
					t.Fatalf("width=%d content=%q: line %q has width %d, want %d",
						width, truncateForError(content), line, got, width)
				}
			}
		}
	}
}

func truncateForError(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

func TestViewHasTopAndBottomRule(t *testing.T) {
	m := New(testStyles())
	m.Focus()
	lines := m.View(40)
	if len(lines) < 3 {
		t.Fatalf("View returned %d lines, want at least 3 (top rule, content, bottom rule)", len(lines))
	}
	if !strings.Contains(lines[0], "─") {
		t.Fatalf("first line is not a rule: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "─") {
		t.Fatalf("last line is not a rule: %q", lines[len(lines)-1])
	}
}

func TestPlaceholderPresentWhenEmptyGoneAfterTyping(t *testing.T) {
	m := New(testStyles())
	m.Focus()

	empty := strings.Join(m.View(80), "\n")
	if !strings.Contains(empty, "how do I log an error") {
		t.Fatalf("expected placeholder text in empty view, got:\n%s", empty)
	}

	m.SetValue("go")
	typed := strings.Join(m.View(80), "\n")
	if strings.Contains(typed, "how do I log an error") {
		t.Fatalf("placeholder should be gone once text exists, got:\n%s", typed)
	}
	if !strings.Contains(typed, "go") {
		t.Fatalf("typed content missing from view:\n%s", typed)
	}
}

// TestMarkerOnFirstLineOnly checks only the editor's first rendered content
// row carries the marker; every row after it — including a second logical
// line the user typed with Shift+Enter, as here — gets the same-width
// two-space continuation indent instead, matching RenderUserMessage's own
// convention once the line is echoed into the transcript (transcript.go)
// and docs/claude-code-reference.md §2's resize-60.txt citation.
func TestMarkerOnFirstLineOnly(t *testing.T) {
	m := New(testStyles())
	m.Focus()
	m.SetValue("a\nb")
	lines := m.View(40)
	// lines[0] and lines[len-1] are rules; the rest are content lines.
	content := lines[1 : len(lines)-1]
	if len(content) < 2 {
		t.Fatalf("expected at least 2 content lines for a 2-line value, got %d: %v", len(content), content)
	}
	if !strings.HasPrefix(content[0], "❯ ") {
		t.Fatalf("first content line %q does not start with the marker", content[0])
	}
	for _, l := range content[1:] {
		if strings.HasPrefix(l, "❯") {
			t.Fatalf("continuation line %q repeats the marker, want a plain two-space indent", l)
		}
		if !strings.HasPrefix(l, "  ") {
			t.Fatalf("continuation line %q does not start with the two-space indent", l)
		}
	}
}

// TestPlaceholderOneSpaceAfterMarker checks the empty-input placeholder
// starts exactly one space after the marker, with no extra blank cell
// (docs/claude-code-reference.md §2, startup-default-home.txt row 9:
// `❯ Try "how do I log an error?"`).
func TestPlaceholderOneSpaceAfterMarker(t *testing.T) {
	m := New(testStyles())
	m.Focus()
	lines := m.View(80)
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}
	want := "❯ " + DefaultPlaceholder
	got := strings.TrimRight(lines[1], " ")
	if got != want {
		t.Fatalf("empty-input content row = %q, want %q", got, want)
	}
}

func TestCursorOffsetByMarkerAndTopRule(t *testing.T) {
	m := New(testStyles())
	m.Focus()
	m.SetValue("hi")
	c := m.Cursor()
	if c == nil {
		t.Fatal("Cursor() = nil while focused")
	}
	// "hi" -> cursor after 2 runes, plus marker (1) + space (1) = column 4.
	if c.Position.X != 4 {
		t.Fatalf("Cursor().Position.X = %d, want 4", c.Position.X)
	}
	if c.Position.Y != 1 {
		t.Fatalf("Cursor().Position.Y = %d, want 1 (below the top rule)", c.Position.Y)
	}
}

func TestScrollIndicatorAtSmallAndLargeWidths(t *testing.T) {
	for _, width := range widthsUnderTest {
		m := New(testStyles())
		m.Focus()
		lines := make([]string, 12)
		for i := range lines {
			lines[i] = "x"
		}
		m.SetValue(strings.Join(lines, "\n"))
		view := m.View(width)
		for _, line := range view {
			if got := ansi.StringWidth(line); got != width {
				t.Fatalf("width=%d: scrolled view line %q has width %d", width, line, got)
			}
		}
		joined := strings.Join(view, "\n")
		if !strings.Contains(joined, "more") {
			t.Fatalf("width=%d: expected a scroll indicator, got:\n%s", width, joined)
		}
	}
}
