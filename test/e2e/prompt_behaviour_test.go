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

// TestPrompt_PlanModeAddsPlanningSteps: in plan mode the structured
// planning steps follow the base prompt.
// Break: drop agent.PlanModePrompt from buildSystemPrompt -> fails.
func TestPrompt_PlanModeAddsPlanningSteps(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	sys := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "", nil, "--permission-mode", "plan")
	base := strings.Index(sys, "You are kiln")
	plan := strings.Index(sys, "You are in PLAN MODE.")
	if base < 0 || plan < 0 || plan < base {
		t.Fatalf("want base prompt then plan-mode prompt; base at %d, plan at %d:\n%s", base, plan, sys)
	}
	if !strings.Contains(sys, "rule by rule") {
		t.Errorf("plan-mode prompt is missing its check step:\n%s", sys)
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
