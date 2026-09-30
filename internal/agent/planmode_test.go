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
