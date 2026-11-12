package tui

import (
	"slices"
	"strings"
	"testing"
)

// TestFullscreenViewport_HorizontalClipAndWide holds the viewport to the
// direct one when rows are cut to the width at every horizontal offset,
// including through a double-width glyph, and when a row holds a tab (which
// lipgloss rewrites, so the padding shortcut must stand aside).
func TestFullscreenViewport_HorizontalClipAndWide(t *testing.T) {
	withRenderEnv(t, 80)
	rows := []string{
		"short",
		strings.Repeat("日本語", 30),
		KilnAmber(strings.Repeat("styled ", 20)),
		"tab\there",
		strings.Repeat("日", 25),
		"",
	}
	for _, rowSet := range [][]string{rows, rows[:3], rows[4:]} {
		m := newTestModel()
		m.width, m.height = 40, 15
		m.fullscreen = true
		m = m.appendTranscript(rowSet)
		m.viewport.SetWidth(40)
		m.viewport.SetHeight(8)
		for x := 0; x < 60; x += 2 {
			vm := m
			vm.viewport.SetXOffset(x)
			tail := []string{"tail  日本語 " + strings.Repeat("z", 50)}
			got, gotTop := vm.fullscreenViewport(tail)
			want, wantTop := vm.fullscreenViewportFull(tail)
			if gotTop != wantTop || !slices.Equal(got, want) {
				t.Fatalf("rows=%d xoffset=%d:\n got  %q\n want %q", len(rowSet), x, got, want)
			}
		}
	}
}

// TestFullscreenViewport_PaddingShortcut pins the mechanism: a plain frame is
// padded from the recorded widths without the viewport (the widths slice
// stays in step with the transcript), and a tab row falls back to it.
func TestFullscreenViewport_PaddingShortcut(t *testing.T) {
	withRenderEnv(t, 80)
	m := newTestModel()
	m.width, m.height = 40, 15
	m.fullscreen = true
	m = m.appendTranscript([]string{"one", "日本", KilnAmber("three")})
	if len(m.transcriptWidths) != len(m.transcript) || m.transcriptWidths[1] != 4 || m.transcriptWidths[2] != 5 {
		t.Fatalf("transcriptWidths = %v for %q", m.transcriptWidths, m.transcript)
	}
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(6)
	if _, ok := padViewportRows(m.transcript, m.transcriptWidths, 40, 6, m.viewport); !ok {
		t.Fatal("a plain viewport did not take the padding shortcut")
	}
	if _, ok := padViewportRows([]string{"a\tb"}, []int{3}, 40, 6, m.viewport); ok {
		t.Fatal("a tab row took the padding shortcut; lipgloss rewrites tabs")
	}
	m = m.clearTranscript()
	if len(m.transcriptWidths) != 0 {
		t.Fatalf("clearTranscript left widths %v", m.transcriptWidths)
	}
}
