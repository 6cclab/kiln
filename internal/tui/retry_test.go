package tui

import (
	"testing"
	"time"
)

func TestRenderRetry_Countdown(t *testing.T) {
	withRenderEnv(t, 80)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	v := RetryView{Message: "Stream interrupted · 529 overloaded", Attempt: 1, Max: 4, Until: base.Add(3 * time.Second)}

	// Three `now` values spanning the countdown: with the full delay left,
	// partway through, and right at zero — RenderRetry must never show a
	// negative countdown at or past Until.
	lines := append([]string{},
		RenderRetry(v, base, 80)...)
	lines = append(lines, "---")
	lines = append(lines, RenderRetry(v, base.Add(1500*time.Millisecond), 80)...)
	lines = append(lines, "---")
	lines = append(lines, RenderRetry(v, base.Add(5*time.Second), 80)...)

	assertRenderGolden(t, "error-countdown", lines)
}

func TestRenderRetry_AttemptNumbering(t *testing.T) {
	withRenderEnv(t, 80)
	now := time.Now()
	// Attempt is the 1-based attempt that just failed; the countdown row
	// must report the NEXT attempt (Attempt+1) as the one about to run.
	v := RetryView{Message: "Request failed", Attempt: 1, Max: 4, Until: now.Add(2 * time.Second)}
	lines := RenderRetry(v, now, 80)
	if len(lines) != 3 {
		t.Fatalf("RenderRetry returned %d lines, want 3", len(lines))
	}
	if got, want := VisibleWidth(lines[2]) > 0, true; got != want {
		t.Fatalf("countdown row is empty")
	}
}
