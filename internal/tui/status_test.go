package tui

import (
	"strings"
	"testing"
	"time"
)

func intPtr(n int) *int { return &n }

func baseState() StatusState {
	return StatusState{
		ModelLabel:    "ollama/qwen3.8",
		ContextWindow: 49_152,
		Mode:          "auto",
		StartedAt:     time.UnixMilli(0),
		Now:           time.UnixMilli(3_600_000),
	}
}

func joined(s StatusState) string {
	rows := RenderStatus(s)
	return rows[0] + "\n" + rows[1]
}

func TestCompact(t *testing.T) {
	cases := map[int]string{
		450_000:   "450k",
		1_000_000: "1.0m",
		49_152:    "49k",
		1_500:     "1.5k",
		900:       "900",
	}
	for n, want := range cases {
		if got := Compact(n); got != want {
			t.Errorf("Compact(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestMeter(t *testing.T) {
	if got := Meter(0, 4); got != "░░░░" {
		t.Errorf("Meter(0,4) = %q", got)
	}
	if got := Meter(1, 4); got != "████" {
		t.Errorf("Meter(1,4) = %q", got)
	}
	if got := Meter(0.5, 4); got != "██░░" {
		t.Errorf("Meter(0.5,4) = %q", got)
	}
	if Meter(0.99, 4) == Meter(1, 4) {
		t.Error("99% must not look identical to done")
	}
	// Meter's cells are multi-byte runes (█/░ are 3 bytes each in UTF-8),
	// so the clamp check must count visible cells, not bytes.
	if VisibleWidth(Meter(5, 4)) != 4 {
		t.Error("Meter(5,4) should clamp to width 4")
	}
	if got := Meter(-1, 4); got != "░░░░" {
		t.Errorf("Meter(-1,4) = %q", got)
	}
}

func TestElapsed(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "30s",
		5 * time.Minute:  "5m",
		90 * time.Minute: "1h 30m",
		3 * time.Hour:    "3h",
	}
	for d, want := range cases {
		if got := Elapsed(d); got != want {
			t.Errorf("Elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestRenderStatusTwoRows(t *testing.T) {
	rows := RenderStatus(baseState())
	if !strings.Contains(rows[1], "auto mode") {
		t.Errorf("mode row = %q, want to contain %q", rows[1], "auto mode")
	}
	if !strings.Contains(rows[1], "shift+tab") {
		t.Error("the key that cycles the mode is not shown")
	}
}

func TestRenderStatusContext(t *testing.T) {
	s := baseState()
	s.ContextUsed = intPtr(22_100)
	out := joined(s)
	for _, want := range []string{"22k", "49k", "45%"} {
		if !strings.Contains(out, want) {
			t.Errorf("status %q missing %q", out, want)
		}
	}
}

func TestRenderStatusGitDirty(t *testing.T) {
	clean := baseState()
	clean.Git = &GitStatus{Branch: "main", Dirty: false}
	dirty := baseState()
	dirty.Git = &GitStatus{Branch: "main", Dirty: true}

	cleanOut, dirtyOut := joined(clean), joined(dirty)
	if cleanOut == dirtyOut {
		t.Error("clean and dirty status must render differently")
	}
	if !strings.Contains(cleanOut, "main") || !strings.Contains(dirtyOut, "main") {
		t.Error("branch name missing")
	}
}

func TestRenderStatusCostOmittedWhenFree(t *testing.T) {
	if strings.Contains(joined(baseState()), "$") {
		t.Error("cost should be omitted with no Cost set")
	}
	zero := baseState()
	zero.Cost = 0
	if strings.Contains(joined(zero), "$") {
		t.Error("cost should be omitted when zero")
	}
	paid := baseState()
	paid.Cost = 170.32
	if !strings.Contains(joined(paid), "$170.32") {
		t.Error("cost should render when nonzero")
	}
}

func TestRenderStatusNoGit(t *testing.T) {
	if strings.Contains(joined(baseState()), "⎇") {
		t.Error("git segment should be omitted with no Git status")
	}
}

func TestRenderStatusThinking(t *testing.T) {
	if strings.Contains(joined(baseState()), "thinking") {
		t.Error("thinking should be omitted while idle")
	}
	s := baseState()
	s.Thinking = true
	if !strings.Contains(joined(s), "thinking") {
		t.Error("thinking indicator missing while reasoning")
	}
}

func TestRenderStatusElapsedAndClock(t *testing.T) {
	if !strings.Contains(joined(baseState()), "1h") {
		t.Error("session age missing")
	}
}

func TestRenderStatusEveryMode(t *testing.T) {
	for _, mode := range []string{"manual", "auto", "acceptEdits", "plan", "bypassPermissions"} {
		s := baseState()
		s.Mode = mode
		rows := RenderStatus(s)
		if !strings.Contains(rows[1], mode) {
			t.Errorf("mode %q not shown in %q", mode, rows[1])
		}
	}
}
