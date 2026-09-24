package tui

import (
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
