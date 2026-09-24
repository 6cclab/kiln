package agent

import (
	"strings"
	"testing"
)

func TestPlanModePromptMatchesTS(t *testing.T) {
	want := strings.Join([]string{
		"You are in PLAN MODE. You may read files, search, and run read-only commands,",
		"but you must not edit, write, or run anything that changes state.",
		"",
		"Research the task thoroughly first. When you have a concrete plan, call",
		"exit_plan_mode with it and wait for approval. Do not attempt changes before",
		"the plan is approved - they will be refused.",
		"",
		"If the user only asked a question, answer it; do not present a plan.",
	}, "\n")
	if PlanModePrompt != want {
		t.Fatalf("PlanModePrompt diverged from the plan-mode.ts source:\n%s", PlanModePrompt)
	}
}

func TestPlanControllerStartsActiveAndApproveLeavesIt(t *testing.T) {
	var gotMode string
	var calls int
	c := NewPlanController()
	c.OnApprove = func(mode string) { calls++; gotMode = mode }

	if !c.IsActive() {
		t.Fatal("a fresh controller must start in plan mode")
	}

	c.Approve("acceptEdits")

	if c.IsActive() {
		t.Fatal("Approve did not leave plan mode")
	}
	if calls != 1 {
		t.Fatalf("OnApprove called %d times, want 1", calls)
	}
	if gotMode != "acceptEdits" {
		t.Fatalf("OnApprove mode = %q, want %q", gotMode, "acceptEdits")
	}
}

func TestPlanControllerSetActive(t *testing.T) {
	c := NewPlanController()
	c.SetActive(false)
	if c.IsActive() {
		t.Fatal("SetActive(false) did not take effect")
	}
	c.SetActive(true)
	if !c.IsActive() {
		t.Fatal("SetActive(true) did not take effect")
	}
}
