package clamp

import "testing"

func TestClampLow(t *testing.T) {
	if got := ClampLow(-5); got != 0 {
		t.Fatalf("ClampLow(-5) = %d, want 0", got)
	}
	if got := ClampLow(5); got != 5 {
		t.Fatalf("ClampLow(5) = %d, want 5", got)
	}
}

func TestClampHigh(t *testing.T) {
	if got := ClampHigh(15, 10); got != 10 {
		t.Fatalf("ClampHigh(15, 10) = %d, want 10", got)
	}
	if got := ClampHigh(5, 10); got != 5 {
		t.Fatalf("ClampHigh(5, 10) = %d, want 5", got)
	}
}
