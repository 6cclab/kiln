package tui

import (
	"regexp"
	"testing"
)

func TestRenderPlanStatuses(t *testing.T) {
	out := RenderPlan([]TodoView{
		{Content: "a", Status: TodoCompletedStatus},
		{Content: "b", Status: TodoInProgressStatus},
		{Content: "c", Status: TodoPendingStatus},
	}, 80)
	// kiln's "plan" block: label rule ("plan" + "d/n" meta) + "Plan · d of
	// n" header + 3 items.
	if len(out) != 5 {
		t.Fatalf("got %d lines, want 5: %v", len(out), out)
	}
	if got := stripANSI(out[1]); got != "Plan · 1 of 3" {
		t.Errorf("header = %q, want %q", got, "Plan · 1 of 3")
	}
}

// TestRenderPlanDoneItemDimThroughout: a done item's text used to lose the
// dim colour after its first character — Muted(Strike(s)) let each
// struck cell's reset cancel the outer colour, so the rest drew in the
// terminal's default foreground (qa/findings *plan-done-text-default-fg).
func TestRenderPlanDoneItemDimThroughout(t *testing.T) {
	prev := enabled
	t.Cleanup(func() { SetColorEnabled(prev); resetTextTokensToDesign() })
	SetColorEnabled(true)
	resetTextTokensToDesign()
	row := RenderPlan([]TodoView{{Content: "add it", Status: TodoCompletedStatus}}, 80)[2]
	dim := "38;2;163;151;129"
	for _, cell := range []string{"a", "d", "i", "t"} {
		if !regexp.MustCompile(`\x1b\[` + dim + `[;0-9]*m` + cell).MatchString(row) {
			t.Errorf("cell %q of the done item is not drawn dim: %q", cell, row)
		}
	}
}
