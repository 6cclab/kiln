package tui

import "testing"

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
