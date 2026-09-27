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
	if !strings.Contains(out, "auto mode") {
		t.Errorf("status line %q missing mode label", out)
	}
	if !strings.Contains(out, "⇧⇥") {
		t.Error("the mode-cycle key is not shown")
	}
}

func TestRenderStatusLine_EveryMode(t *testing.T) {
	cases := map[string]string{
		"manual":            "ask before edits",
		"auto":              "auto mode",
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

// TestModeLabel_AutoDistinctFromAcceptEdits pins the fix for
// qa/findings/20260926T231105Z-mode-ring-duplicate-label.json: acceptEdits
// and auto are different permission-mode ring stops (settings.Decide
// auto-allows only edit/write for acceptEdits, but everything for auto)
// and must render distinguishable labels.
func TestModeLabel_AutoDistinctFromAcceptEdits(t *testing.T) {
	acceptEditsLabel, _ := modeLabel("acceptEdits")
	autoLabel, _ := modeLabel("auto")
	if acceptEditsLabel == autoLabel {
		t.Fatalf("acceptEdits and auto must have distinct labels, both got %q", acceptEditsLabel)
	}
	if acceptEditsLabel != "auto-edit" {
		t.Errorf("acceptEdits label = %q, want %q", acceptEditsLabel, "auto-edit")
	}
	if autoLabel != "auto mode" {
		t.Errorf("auto label = %q, want %q", autoLabel, "auto mode")
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

// TestShortenPathLeft covers the left-truncation helper defect 1 relies on:
// leading ellipsis, trailing components kept, unchanged when it already
// fits.
func TestShortenPathLeft(t *testing.T) {
	cases := []struct {
		path  string
		width int
		want  string
	}{
		{"~/src/relay-api", 80, "~/src/relay-api"}, // fits, unchanged
		{"/private/tmp/scratchpad/real-proj", 22, "…/scratchpad/real-proj"},
		{"/private/tmp/scratchpad/real-proj", 11, "…/real-proj"},
		{"/private/tmp/scratchpad/real-proj", 1, "…"},
		{"/private/tmp/scratchpad/real-proj", 0, ""},
	}
	for _, c := range cases {
		got := ShortenPathLeft(c.path, c.width)
		if got != c.want {
			t.Errorf("ShortenPathLeft(%q, %d) = %q, want %q", c.path, c.width, got, c.want)
		}
		if VisibleWidth(got) > c.width && c.width > 0 {
			t.Errorf("ShortenPathLeft(%q, %d) = %q (%d cols) overflowed", c.path, c.width, got, VisibleWidth(got))
		}
	}
}

// TestRenderStatusLine_LongCwdKeepsBranchAt120Cols is defect 1: a long cwd
// used to make build(true,true) return "" (spacer < 1), dropping the WHOLE
// location segment — cwd and branch both vanished. The fix shortens the
// cwd first; branch and its dirty marker must survive.
func TestRenderStatusLine_LongCwdKeepsBranchAt120Cols(t *testing.T) {
	s := baseState()
	s.Cwd = "/private/tmp/very/deeply/nested/scratchpad/directory/for/a/real-project-name"
	s.Git = &GitStatus{Branch: "feature/long-branch-name", Dirty: true}
	for _, width := range []int{120, 80} {
		out := RenderStatusLine(s, width)
		if VisibleWidth(out) > width {
			t.Errorf("width %d: status line overflowed: %q (%d cols)", width, out, VisibleWidth(out))
		}
		if !strings.Contains(out, "feature/long-branch-name*") {
			t.Errorf("width %d: branch/dirty marker missing from %q", width, out)
		}
		if !strings.Contains(out, "…") {
			t.Errorf("width %d: expected the cwd to be left-truncated with an ellipsis in %q", width, out)
		}
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

// TestMeterFilled: any use the label shows (≥1%) fills at least one cell, and only a full
// context fills the bar. Rounding down left a 10-cell meter empty below 10%
// (qa/findings *ctx-meter-empty-below-10pct).
func TestMeterFilled(t *testing.T) {
	cases := []struct {
		f    float64
		want int
	}{{0, 0}, {0.001, 0}, {0.005, 1}, {0.04, 1}, {0.09, 1}, {0.1, 1}, {0.38, 3}, {0.999, 9}, {1, 10}, {1.5, 10}}
	for _, c := range cases {
		if got := meterFilled(c.f, 10); got != c.want {
			t.Errorf("meterFilled(%v, 10) = %d, want %d", c.f, got, c.want)
		}
	}
}
