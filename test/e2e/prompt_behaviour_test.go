//go:build e2e

// Package e2e: system prompt behaviour. These check the system prompt the
// real kiln binary sends, not a replica of its assembly. Each test's doc
// comment records the break used to confirm it can fail.
package e2e

import (
	"strings"
	"testing"
)

// TestPrompt_DefaultCarriesBasePrompt: a run with no --system-prompt sends
// agent.BasePrompt's working habits, not the old one-line persona.
// Break: set chat.go's defaultSystemPrompt back to "You are a coding
// assistant operating in a terminal." -> fails on every phrase.
func TestPrompt_DefaultCarriesBasePrompt(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	sys := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "", nil)
	for _, want := range []string{"You are kiln", "fails without the fix", "review `git status` and the diff"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt is missing %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "PLAN MODE") {
		t.Errorf("plan-mode prompt sent outside plan mode")
	}
}

// TestPrompt_PlanModeAddsPlanningSteps: a session started in plan mode
// sends the plan-mode steps as a reminder ahead of the first prompt, not in
// the system prompt (which must not change when the mode does).
// Break: drop reminders.Prefix from runPrintMode -> fails.
func TestPrompt_PlanModeAddsPlanningSteps(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	first := budgetRunAndFirstMessages(t, home, sessDir, proj, "faux/faux-1", "", nil, "--permission-mode", "plan")
	reminder := strings.Index(first, "You are in PLAN MODE.")
	prompt := strings.Index(first, "hello")
	if reminder < 0 || prompt < reminder || !strings.Contains(first, `system-reminder\u003e`) {
		t.Fatalf("want the plan-mode reminder ahead of the prompt; reminder at %d, prompt at %d:\n%s", reminder, prompt, first)
	}
	if !strings.Contains(first, "rule by rule") {
		t.Errorf("plan-mode reminder is missing its check step:\n%s", first)
	}
}

// TestPrompt_SystemPromptFlagReplacesBase: --system-prompt replaces the
// base prompt, as documented.
// Break: always prepend agent.BasePrompt -> fails.
func TestPrompt_SystemPromptFlagReplacesBase(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	sys := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "", nil, "--system-prompt", "You are a test persona.")
	if !strings.HasPrefix(sys, "You are a test persona.") || strings.Contains(sys, "You are kiln") {
		t.Fatalf("--system-prompt did not replace the base prompt:\n%s", sys)
	}
}
