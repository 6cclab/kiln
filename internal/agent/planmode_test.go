package agent

import (
	"strings"
	"testing"
)

// The plan-mode prompt keeps the read-only rule and the question escape,
// and gives planning its steps: check against the spec, sort findings, ask,
// present. It deliberately diverges from plan-mode.ts (see PlanModePrompt).
func TestPlanModePromptStructuresPlanning(t *testing.T) {
	for _, want := range []string{
		"You are in PLAN MODE.",
		"must not edit, write, or run anything that changes state",
		"If the user only asked a question, answer it; do not present a plan.",
		"rule by rule",
		"ask_user_question",
		"Call exit_plan_mode with the plan and wait for approval.",
	} {
		if !strings.Contains(PlanModePrompt, want) {
			t.Errorf("PlanModePrompt is missing %q", want)
		}
	}
}

// BasePrompt carries the working habits every task needs; each phrase
// guards one habit the one-line persona left to chance.
func TestBasePromptCarriesWorkingHabits(t *testing.T) {
	for _, want := range []string{
		"You are kiln",
		"check the code\nagainst each rule",
		"match its naming",
		"well-established library",
		"check that before designing around it",
		"ask_user_question",
		"fails without the fix",
		"Report what actually happened",
		"review `git status` and the diff",
		"read what you are about to\ndelete or overwrite",
	} {
		if !strings.Contains(BasePrompt, want) {
			t.Errorf("BasePrompt is missing %q", want)
		}
	}
}

// A session started without a system prompt gets BasePrompt.
func TestDefaultSystemPromptIsBasePrompt(t *testing.T) {
	if defaultSystemPrompt != BasePrompt {
		t.Fatalf("defaultSystemPrompt = %q, want BasePrompt", defaultSystemPrompt)
	}
}

// TestPlanControllerFollowsGateMode: the controller has no plan state of
// its own; it reports whatever its source says, however that changed, and
// Approve only hands the chosen mode to OnApprove.
func TestPlanControllerFollowsGateMode(t *testing.T) {
	inPlan := false
	c := NewPlanController(func() bool { return inPlan })
	var gotMode string
	var calls int
	c.OnApprove = func(mode string) { calls++; gotMode = mode; inPlan = false }

	if c.IsActive() {
		t.Fatal("controller reports plan mode while its source does not")
	}
	inPlan = true // e.g. /plan or Shift+Tab moved the gate
	if !c.IsActive() {
		t.Fatal("controller missed plan mode entered after it was built")
	}
	c.Approve("acceptEdits")
	if calls != 1 || gotMode != "acceptEdits" {
		t.Fatalf("OnApprove calls=%d mode=%q, want 1 and acceptEdits", calls, gotMode)
	}
	if c.IsActive() {
		t.Fatal("still in plan mode after OnApprove left it")
	}
	inPlan = true // a second plan in the same session
	if !c.IsActive() {
		t.Fatal("controller missed a second entry into plan mode")
	}
}
