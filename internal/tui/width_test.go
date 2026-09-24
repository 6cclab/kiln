package tui

import (
	"strings"
	"testing"
)

func TestFitStatusTruncatesWithEllipsis(t *testing.T) {
	got := FitStatus(longURL, width40)
	if VisibleWidth(got) > width40 {
		t.Errorf("FitStatus overflowed: %q", got)
	}
	if got == longURL {
		t.Error("expected truncation for an overflowing line")
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected an ellipsis tail, got %q", got)
	}
}

func TestFitStatusPassthrough(t *testing.T) {
	if got := FitStatus("short", width40); got != "short" {
		t.Errorf("got %q, want passthrough", got)
	}
}

func TestFitStatusZeroWidth(t *testing.T) {
	if got := FitStatus("anything", 0); got != "" {
		t.Errorf("zero width should render nothing, got %q", got)
	}
}

func TestFitLinesZeroWidth(t *testing.T) {
	if out := FitLines([]string{"x"}, 0, ""); len(out) != 0 {
		t.Errorf("zero width should render nothing, got %v", out)
	}
}

func TestVisibleWidthIgnoresANSI(t *testing.T) {
	SetColorEnabled(true)
	defer SetColorEnabled(colorEnabled())
	styled := Red("hi")
	if VisibleWidth(styled) != 2 {
		t.Errorf("VisibleWidth(%q) = %d, want 2", styled, VisibleWidth(styled))
	}
}
