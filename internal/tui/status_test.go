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

// TestRenderStatusLine_ShowsModelAfterLocation: the design's status line
// names the active model, in ink, right after cwd · branch and before the
// spacer that pushes ctx and cost to the right edge.
func TestRenderStatusLine_ShowsModelAfterLocation(t *testing.T) {
	s := baseState()
	s.Git = &GitStatus{Branch: "main", Dirty: true}
	out := stripANSI(RenderStatusLine(s, 120))
	loc := strings.Index(out, "~/src/relay-api · main*")
	model := strings.Index(out, "ollama/qwen3.8")
	ctx := strings.Index(out, "ctx ")
	if loc < 0 || model < 0 || ctx < 0 {
		t.Fatalf("missing segment in %q", out)
	}
	if !(loc < model && model < ctx) {
		t.Errorf("want location, then model, then ctx; got %q", out)
	}
	if !strings.Contains(RenderStatusLine(s, 120), Ink("ollama/qwen3.8")) {
		t.Error("model is not styled with the Ink token")
	}
}

// TestRenderStatusLine_ModelFollowsSwitch: the label is the footer state's,
// so a /model switch (MsgModelInfo → StatusPatch.ModelLabel) shows the new
// model, not the one the session started on.
func TestRenderStatusLine_ModelFollowsSwitch(t *testing.T) {
	f := NewFooterState(baseState())
	label := "anthropic/claude-opus-4-8"
	f.Apply(StatusPatch{ModelLabel: &label})
	out := stripANSI(RenderStatusLine(f.State(), 120))
	if !strings.Contains(out, label) || strings.Contains(out, "qwen") {
		t.Errorf("status line after switch = %q, want %s and no qwen", out, label)
	}
}

// TestRenderStatusLine_ModelDropOrder: as the row narrows, cost goes
// first (the model stays), then the model; cost may come back beside the
// location once the model no longer fits, and the location goes last. The
// row never overflows.
func TestRenderStatusLine_ModelDropOrder(t *testing.T) {
	s := baseState()
	s.Cwd = "~/src/a-rather-long/project/path/relay-api"
	s.Git = &GitStatus{Branch: "main"}
	s.Cost = 1.23
	s.ContextUsed = intPtr(1000)
	modelGone, sawModelNoCost := false, false
	for w := 140; w >= 20; w-- {
		out := stripANSI(RenderStatusLine(s, w))
		if VisibleWidth(out) > w {
			t.Fatalf("width %d: overflowed: %q", w, out)
		}
		hasModel := strings.Contains(out, "ollama/qwen3.8")
		hasCost := strings.Contains(out, "$1.23")
		hasLoc := strings.Contains(out, "main")
		if hasModel && modelGone {
			t.Fatalf("width %d: the model came back after it dropped: %q", w, out)
		}
		if !hasModel {
			modelGone = true
		}
		if hasCost && !hasModel && !sawModelNoCost {
			t.Fatalf("width %d: the model dropped before cost did: %q", w, out)
		}
		if hasModel && !hasLoc {
			t.Fatalf("width %d: location dropped while the model is still shown: %q", w, out)
		}
		sawModelNoCost = sawModelNoCost || (hasModel && !hasCost && hasLoc)
	}
	if !sawModelNoCost || !modelGone {
		t.Errorf("drop stages not all reached: model-without-cost=%v model-dropped=%v", sawModelNoCost, modelGone)
	}
}

// TestRenderStatusLine_NoModelLabel: an empty label adds nothing (no
// stray separator).
func TestRenderStatusLine_NoModelLabel(t *testing.T) {
	s := baseState()
	s.ModelLabel = ""
	with := baseState()
	if a, b := stripANSI(RenderStatusLine(s, 120)), stripANSI(RenderStatusLine(with, 120)); a == b || strings.Contains(a, "qwen") {
		t.Errorf("empty label rendered %q", a)
	}
}
