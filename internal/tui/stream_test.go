package tui

import (
	"strings"
	"testing"
)

func TestRenderStreamLive_LastRowsAndCaret(t *testing.T) {
	withRenderEnv(t, 40)

	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, "line "+string(rune('a'+i-1)))
	}
	text := strings.Join(lines, "\n")

	got := RenderStreamLive(text, 40, maxStreamRows)
	// rule + maxStreamRows wrapped rows.
	if len(got) != 1+maxStreamRows {
		t.Fatalf("RenderStreamLive returned %d lines, want %d (rule + %d rows)", len(got), 1+maxStreamRows, maxStreamRows)
	}
	// Only the LAST maxStreamRows source lines should appear — the first
	// (12-8=4) lines are dropped.
	if strings.Contains(got[1], "line a") {
		t.Errorf("first row still shows the earliest line; want only the last %d rows", maxStreamRows)
	}
	last := got[len(got)-1]
	if !strings.Contains(last, G().StreamCaret) {
		t.Errorf("last row = %q, want it to contain the streaming caret %q", last, G().StreamCaret)
	}

	assertRenderGolden(t, "text-streaming", got)
}

func TestRenderStreamLive_Empty(t *testing.T) {
	withRenderEnv(t, 40)
	got := RenderStreamLive("", 40, maxStreamRows)
	if len(got) != 2 {
		t.Fatalf("RenderStreamLive(\"\") returned %d lines, want 2 (rule + caret row)", len(got))
	}
	if !strings.Contains(got[1], G().StreamCaret) {
		t.Errorf("empty-text row = %q, want it to contain the caret", got[1])
	}
}

// TestModel_StreamLive_HiddenInPlainMode checks D's spec that the live
// streaming block never renders under --ax-screen-reader/plain mode (a
// screen reader would otherwise re-read it every tick).
func TestModel_StreamLive_HiddenInPlainMode(t *testing.T) {
	prevPlain := plain
	SetPlainMode(true)
	t.Cleanup(func() { SetPlainMode(prevPlain) })

	m := newTestModel()
	m.streamText = "hello world"
	if rows := m.renderStreamLive(40); rows != nil {
		t.Errorf("renderStreamLive in plain mode = %v, want nil", rows)
	}
}
