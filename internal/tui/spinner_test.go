package tui

import (
	"strings"
	"testing"
	"time"
)

func TestSpinnerIdleRendersNothing(t *testing.T) {
	var s SpinnerState
	if out := s.Render(80, time.Time{}); len(out) != 0 {
		t.Errorf("idle spinner should render nothing, got %v", out)
	}
}

func TestSpinnerBusyRendersOneLine(t *testing.T) {
	var s SpinnerState
	s.Start(1)
	if !s.Busy() {
		t.Fatal("Start should mark the spinner busy")
	}
	out := s.Render(80, time.Time{})
	if len(out) != 1 {
		t.Fatalf("got %d lines, want 1", len(out))
	}
	s.Stop()
	if s.Busy() {
		t.Error("Stop should clear busy")
	}
	if out2 := s.Render(80, time.Time{}); len(out2) != 0 {
		t.Error("stopped spinner should leave no residue")
	}
}

func TestSpinnerLabelResetsToBase(t *testing.T) {
	var s SpinnerState
	s.Start(2)
	base := s.label
	s.SetLabel("Reasoning")
	if s.label != "Reasoning" {
		t.Fatal("SetLabel should override the label")
	}
	s.ResetLabel()
	if s.label != base {
		t.Errorf("ResetLabel should restore %q, got %q", base, s.label)
	}
}

func TestSpinnerTokensReplaceNotAdd(t *testing.T) {
	var s SpinnerState
	s.Start(0)
	s.SetTokens(100)
	s.SetTokens(50)
	if s.Tokens() != 50 {
		t.Errorf("SetTokens should replace, got %d", s.Tokens())
	}
}

func TestRenderSpinnerNoSuffixAtZero(t *testing.T) {
	// kiln's spinner glyphs are ◐◓◑◒ (docs/kiln-design.md); frame 0 is
	// always "◐", including at t=0 with no thinking/tokens yet, which
	// still renders only "<frame> <Label>…", no suffix.
	line := stripANSI(RenderSpinner(SpinnerArgs{Frame: 0, Label: "Whirring", ElapsedSeconds: 0}))
	if line != "◐ Whirring…" {
		t.Errorf("got %q, want %q", line, "◐ Whirring…")
	}
}

func TestRenderSpinnerThinkingSuffix(t *testing.T) {
	line := stripANSI(RenderSpinner(SpinnerArgs{Frame: 0, Label: "Computing", ElapsedSeconds: 1, Thinking: true, Effort: "medium"}))
	if line != "◐ Computing… (1s · thinking with medium effort)" {
		t.Errorf("got %q", line)
	}
}

func TestRenderSpinnerTokenSuffixOverridesThinking(t *testing.T) {
	tokens := 1200
	line := stripANSI(RenderSpinner(SpinnerArgs{Frame: 0, Label: "Crunching", ElapsedSeconds: 4, Thinking: true, Tokens: &tokens}))
	if line != "◐ Crunching… (4s · ↓ 1.2k tokens)" {
		t.Errorf("got %q", line)
	}
}

func TestSpinnerStateLabelAndThinking(t *testing.T) {
	var s SpinnerState
	s.Start(2) // Crunching
	if s.Label() != "Crunching" {
		t.Fatalf("got label %q", s.Label())
	}
	s.SetThinking(true, "high")
	out := stripANSI(strings.Join(s.Render(80, time.Time{}), ""))
	if !strings.Contains(out, "thinking with high effort") {
		t.Errorf("got %q", out)
	}
}
