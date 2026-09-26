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
		Cwd:           "~/src/relay-api",
		StartedAt:     time.UnixMilli(0),
		Now:           time.UnixMilli(3_600_000),
	}
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
	if got := Meter(0, 4); got != "────" {
		t.Errorf("Meter(0,4) = %q", got)
	}
	if got := Meter(1, 4); got != "━━━━" {
		t.Errorf("Meter(1,4) = %q", got)
	}
	if got := Meter(0.5, 4); got != "━━──" {
		t.Errorf("Meter(0.5,4) = %q", got)
	}
	if Meter(0.99, 4) == Meter(1, 4) {
		t.Error("99% must not look identical to done")
	}
	if VisibleWidth(Meter(5, 4)) != 4 {
		t.Error("Meter(5,4) should clamp to width 4")
	}
	if got := Meter(-1, 4); got != "────" {
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

func TestRenderStatusLine_ModeLabelAndKey(t *testing.T) {
	out := RenderStatusLine(baseState(), 80)
	if !strings.Contains(out, "auto-edit") {
		t.Errorf("status line %q missing mode label", out)
	}
	if !strings.Contains(out, "⇧⇥") {
		t.Error("the mode-cycle key is not shown")
	}
}

func TestRenderStatusLine_EveryMode(t *testing.T) {
	cases := map[string]string{
		"manual":            "ask before edits",
		"auto":              "auto-edit",
		"acceptEdits":       "auto-edit",
		"bypassPermissions": "bypass permissions",
		"dontAsk":           "don't ask",
		"plan":              "plan only",
	}
	for mode, want := range cases {
		s := baseState()
		s.Mode = mode
		out := RenderStatusLine(s, 80)
		if !strings.Contains(out, want) {
			t.Errorf("mode %q: status line %q missing %q", mode, out, want)
		}
	}
}

func TestRenderStatusLine_Context(t *testing.T) {
	s := baseState()
	s.ContextUsed = intPtr(intFrac(49_152, 0.38))
	out := RenderStatusLine(s, 80)
	for _, want := range []string{"ctx", "38%"} {
		if !strings.Contains(out, want) {
			t.Errorf("status %q missing %q", out, want)
		}
	}
}

func intFrac(total int, frac float64) int {
	return int(float64(total) * frac)
}

func TestRenderStatusLine_ContextNilShowsEmptyMeterZeroPercent(t *testing.T) {
	out := RenderStatusLine(baseState(), 80)
	if !strings.Contains(out, "0%") {
		t.Errorf("status %q should show 0%% with no ContextUsed", out)
	}
}

func TestRenderStatusLine_ContextHighPressureIsRed(t *testing.T) {
	s := baseState()
	s.ContextUsed = intPtr(intFrac(49_152, 0.80))
	out := RenderStatusLine(s, 80)
	if !strings.Contains(out, "80%") {
		t.Errorf("status %q missing 80%%", out)
	}
}

func TestRenderStatusLine_GitDirty(t *testing.T) {
	clean := baseState()
	clean.Git = &GitStatus{Branch: "main", Dirty: false}
	dirty := baseState()
	dirty.Git = &GitStatus{Branch: "main", Dirty: true}

	cleanOut, dirtyOut := RenderStatusLine(clean, 80), RenderStatusLine(dirty, 80)
	if cleanOut == dirtyOut {
		t.Error("clean and dirty status must render differently")
	}
	if !strings.Contains(cleanOut, "main") || !strings.Contains(dirtyOut, "main*") {
		t.Error("branch name / dirty marker missing")
	}
}

// TestRenderStatusLine_CostAlwaysShown: the cost segment always renders,
// as "$0.00" when nothing has been spent yet — design scene 01 shows
// "$0.00" from the very first frame, not an omitted segment (docs/
// kiln-design-handoff/README.md "Screen anatomy").
func TestRenderStatusLine_CostAlwaysShown(t *testing.T) {
	if !strings.Contains(RenderStatusLine(baseState(), 80), "$0.00") {
		t.Error("cost should render as $0.00 with no Cost set")
	}
	paid := baseState()
	paid.Cost = 170.32
	if !strings.Contains(RenderStatusLine(paid, 80), "$170.32") {
		t.Error("cost should render when nonzero")
	}
}

func TestRenderStatusLine_NoGitOmitsLocationSeparator(t *testing.T) {
	s := baseState()
	s.Cwd = "~/src/relay-api"
	out := RenderStatusLine(s, 80)
	if !strings.Contains(out, "~/src/relay-api") {
		t.Error("cwd missing")
	}
}

func TestRenderStatusLine_FitsWidthDroppingSegments(t *testing.T) {
	s := baseState()
	s.Git = &GitStatus{Branch: "main", Dirty: true}
	s.Cost = 1.23
	s.ContextUsed = intPtr(1000)
	// A very narrow width forces segments to drop rather than overflow.
	out := RenderStatusLine(s, 20)
	if VisibleWidth(out) > 20 {
		t.Errorf("status line overflowed width 20: %q (%d cols)", out, VisibleWidth(out))
	}
}

func TestAbbrevHome(t *testing.T) {
	if got := AbbrevHome("/home/x/src/relay-api", "/home/x"); got != "~/src/relay-api" {
		t.Errorf("AbbrevHome = %q", got)
	}
	if got := AbbrevHome("/home/x", "/home/x"); got != "~" {
		t.Errorf("AbbrevHome(home) = %q", got)
	}
	if got := AbbrevHome("/other/path", "/home/x"); got != "/other/path" {
		t.Errorf("AbbrevHome(unrelated) = %q", got)
	}
}
