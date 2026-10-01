package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Key handling here mirrors permission-prompt.ts:97-189 exactly (see
// permissionview.go's doc comment); these tests exercise that contract
// directly against PromptState, without a PTY.

// genericToolName is any tool name promptOptionsFor does not special-case
// (not "bash", "edit" or "write"), so it renders/handles as the 3-option
// generic prompt (Yes / don't-ask-again / No-and-tell-kiln) these tests
// exercise.
const genericToolName = "grep"

func TestPromptState_ToolAllow(t *testing.T) {
	for _, k := range []string{"1", "y", "enter"} {
		p := NewPromptState("/tmp")
		reply := p.AskTool(PermissionRequest{Grantable: true, ToolName: genericToolName})
		if !p.HandleKey(key(k)) {
			t.Fatalf("key %q not consumed", k)
		}
		choice := <-reply
		if choice.Kind != ChoiceAllow {
			t.Errorf("key %q: kind = %q, want allow", k, choice.Kind)
		}
	}
}

func TestPromptState_ToolAllowAlways(t *testing.T) {
	for _, k := range []string{"2", "a"} {
		p := NewPromptState("/tmp")
		reply := p.AskTool(PermissionRequest{Grantable: true, ToolName: genericToolName})
		p.HandleKey(key(k))
		if choice := <-reply; choice.Kind != ChoiceAllowAlways {
			t.Errorf("key %q: kind = %q, want allow-always", k, choice.Kind)
		}
	}
}

func TestPromptState_ToolDenyWithFeedback(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, ToolName: genericToolName})

	p.HandleKey(key("3")) // enters feedback mode
	if !p.Active() {
		t.Fatal("prompt cleared entering feedback mode")
	}
	for _, r := range "do X instead" {
		p.HandleKey(key(string(r)))
	}
	p.HandleKey(key("enter"))

	choice := <-reply
	if choice.Kind != ChoiceDeny || choice.Feedback != "do X instead" {
		t.Errorf("choice = %+v, want deny with feedback", choice)
	}
}

func TestPromptState_ToolEscDeniesOutright(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, ToolName: genericToolName})
	p.HandleKey(key("esc"))
	choice := <-reply
	if choice.Kind != ChoiceDeny || choice.Feedback != "" {
		t.Errorf("choice = %+v, want a bare deny", choice)
	}
}

func TestPromptState_FeedbackBackspace(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, ToolName: genericToolName})
	p.HandleKey(key("3"))
	p.HandleKey(key("x"))
	p.HandleKey(backspaceKey())
	p.HandleKey(key("enter"))
	choice := <-reply
	if choice.Kind != ChoiceDeny || choice.Feedback != "" {
		t.Errorf("choice = %+v, want empty feedback after backspacing the only char", choice)
	}
}

func TestPromptState_UnknownKeySwallowedWhileActive(t *testing.T) {
	p := NewPromptState("/tmp")
	p.AskTool(PermissionRequest{Grantable: true, ToolName: genericToolName})
	if !p.HandleKey(key("z")) {
		t.Error("unknown key not swallowed while a prompt is active")
	}
	if !p.Active() {
		t.Error("prompt closed by an unrelated key")
	}
}

// --- Bash's 4-option variant -------------------------------------------

func TestPromptState_BashAllow(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, DontAskRules: []string{"npm test *"}, ToolName: "bash"})
	p.HandleKey(key("1"))
	if choice := <-reply; choice.Kind != ChoiceAllow {
		t.Errorf("choice = %+v, want allow", choice)
	}
}

func TestPromptState_BashAllowAlways(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, DontAskRules: []string{"npm test *"}, ToolName: "bash"})
	p.HandleKey(key("2"))
	if choice := <-reply; choice.Kind != ChoiceAllowAlways {
		t.Errorf("choice = %+v, want allow-always", choice)
	}
}

// TestPromptState_BashSwitchToAutoThenAllow presses "3" ("Yes, and switch
// to auto mode") and checks both halves of what that option promises: the
// pending call is allowed, and PromptState leaves the requested mode on
// switchMode for app.go to apply to the real gate (permissionview.go does
// not have a gate reference itself).
func TestPromptState_BashSwitchToAutoThenAllow(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, DontAskRules: []string{"npm test *"}, ToolName: "bash"})
	p.HandleKey(key("3"))
	if choice := <-reply; choice.Kind != ChoiceAllow {
		t.Errorf("choice = %+v, want allow", choice)
	}
	if p.switchMode != "auto" {
		t.Errorf("switchMode = %q, want %q", p.switchMode, "auto")
	}
}

func TestPromptState_BashNoDeniesOutright(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, DontAskRules: []string{"npm test *"}, ToolName: "bash"})
	p.HandleKey(key("4"))
	choice := <-reply
	if choice.Kind != ChoiceDeny || choice.Feedback != "" {
		t.Errorf("choice = %+v, want a bare deny (no feedback capture)", choice)
	}
	if p.Active() {
		t.Error("bash prompt stayed active after key 4; want it to deny outright")
	}
}

// TestPromptState_BashTabOpensFeedback pins finding
// tab-to-amend-not-implemented: the Bash prompt's hint row advertises
// "tab to amend", but Bash's option list (promptOptionsFor's
// optAllow/optAllowAlways/optSwitchAutoAllow/optDenyOutright) has no
// feedback option at all, so before this fix Tab fell into HandleKey's
// digit-key default case, which does nothing for a non-digit key: the
// prompt stayed open, unconsumed characters typed afterward leaked to the
// editor behind it, and Enter ran whichever option was already
// highlighted instead of declining. Tab must now open the same inline
// feedback field the generic prompt's "3"/"n" already opens, and Enter
// there must send a decline carrying the typed reason.
func TestPromptState_BashTabOpensFeedback(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, DontAskRules: []string{"npm test *"}, ToolName: "bash", PrimaryArg: "echo tab-amend-probe"})

	if !p.HandleKey(key("tab")) {
		t.Fatal("tab not consumed by the bash prompt")
	}
	if p.feedback == nil {
		t.Fatal("tab did not open the feedback field")
	}
	for _, r := range "please explain what this does first" {
		if !p.HandleKey(key(string(r))) {
			t.Fatalf("feedback char %q not consumed", r)
		}
	}
	// The field opens inside the bash prompt, not the generic one
	// (qa/findings *bash-feedback-reframes).
	if view := strings.Join(p.Render(100), "\n"); !strings.Contains(view, "Allow kiln to run this command?") || strings.Contains(view, "use bash?") {
		t.Errorf("feedback view left the bash prompt:\n%s", view)
	}
	p.HandleKey(key("enter"))

	choice := <-reply
	if choice.Kind != ChoiceDeny {
		t.Errorf("choice.Kind = %q, want deny", choice.Kind)
	}
	if choice.Feedback != "please explain what this does first" {
		t.Errorf("choice.Feedback = %q, want the typed reason", choice.Feedback)
	}
}

// TestPromptState_BashTabFromAnyOptionOpensFeedback checks tab is not
// specific to the "No" row — it opens feedback capture regardless of
// which option is currently highlighted, matching Claude Code's own
// tab-to-amend (any option, not just the last one).
func TestPromptState_BashTabFromAnyOptionOpensFeedback(t *testing.T) {
	p := NewPromptState("/tmp")
	p.AskTool(PermissionRequest{Grantable: true, DontAskRules: []string{"npm test *"}, ToolName: "bash"})
	p.HandleKey(key("down")) // selected=1 ("don't ask again")
	p.HandleKey(key("tab"))
	if p.feedback == nil {
		t.Fatal("tab from a non-'No' option did not open the feedback field")
	}
}

// TestPromptState_EditTabOpensFeedback checks the same fix for the
// Edit/Write 3-option variant, which also advertises "tab to amend" with
// no numbered feedback option of its own.
func TestPromptState_EditTabOpensFeedback(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{ToolName: "edit", PrimaryArg: "src/math.js"})
	p.HandleKey(key("tab"))
	if p.feedback == nil {
		t.Fatal("tab did not open the feedback field on the edit prompt")
	}
	for _, r := range "typo" {
		p.HandleKey(key(string(r)))
	}
	p.HandleKey(key("enter"))
	choice := <-reply
	if choice.Kind != ChoiceDeny || choice.Feedback != "typo" {
		t.Errorf("choice = %+v, want deny with feedback %q", choice, "typo")
	}
}

// TestPromptState_TabEscCancelsBackToOptions checks Esc from the
// tab-opened feedback field declines outright (same as Esc on the
// options menu), not a decline "with empty feedback" distinguishable
// from a bare Esc.
func TestPromptState_TabEscCancelsBackToOptions(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, DontAskRules: []string{"npm test *"}, ToolName: "bash"})
	p.HandleKey(key("tab"))
	p.HandleKey(key("x"))
	p.HandleKey(key("esc"))
	choice := <-reply
	if choice.Kind != ChoiceDeny || choice.Feedback != "" {
		t.Errorf("choice = %+v, want a bare deny", choice)
	}
}

func TestPromptState_BashArrowNavigation(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{Grantable: true, DontAskRules: []string{"npm test *"}, ToolName: "bash"})
	p.HandleKey(key("down"))
	p.HandleKey(key("down"))
	if p.pending.selected != 2 {
		t.Fatalf("selected = %d, want 2 after two downs", p.pending.selected)
	}
	p.HandleKey(key("enter"))
	choice := <-reply
	if choice.Kind != ChoiceAllow || p.switchMode != "auto" {
		t.Errorf("choice = %+v, switchMode = %q, want allow + switch to auto (option index 2)", choice, p.switchMode)
	}
}

// --- Edit/Write's 3-option variant ---------------------------------------

func TestPromptState_EditSwitchToAcceptEditsThenAllow(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{ToolName: "edit"})
	p.HandleKey(key("2"))
	if choice := <-reply; choice.Kind != ChoiceAllow {
		t.Errorf("choice = %+v, want allow", choice)
	}
	if p.switchMode != "acceptEdits" {
		t.Errorf("switchMode = %q, want %q", p.switchMode, "acceptEdits")
	}
}

func TestPromptState_EditNoDeniesOutright(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{ToolName: "write"})
	p.HandleKey(key("3"))
	choice := <-reply
	if choice.Kind != ChoiceDeny || choice.Feedback != "" {
		t.Errorf("choice = %+v, want a bare deny (Edit/Write has no feedback option)", choice)
	}
}

// TestPromptState_PlanApproveAutoMode: option 1 reads "Yes, and use auto
// mode" and must enter auto mode, by key or by enter on the default row. It
// used to set acceptEdits, so the approved plan then stopped for every bash
// command (qa/findings *plan-approval-auto-mode-sets-accept-edits).
func TestPromptState_PlanApproveAutoMode(t *testing.T) {
	for _, k := range []string{"1", "enter"} {
		p := NewPromptState("/tmp")
		reply := p.AskPlan("do the thing", "~/.harness/plans/test.md")
		p.HandleKey(key(k))
		decision := <-reply
		if decision.Kind != PlanApprove || decision.Mode != "auto" {
			t.Errorf("%s: decision = %+v, want approve/auto", k, decision)
		}
	}
}

func TestPromptState_PlanApproveManual(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskPlan("do the thing", "~/.harness/plans/test.md")
	p.HandleKey(key("2"))
	decision := <-reply
	if decision.Kind != PlanApprove || decision.Mode != "manual" {
		t.Errorf("decision = %+v, want approve/manual", decision)
	}
}

func TestPromptState_PlanReviseWithFeedback(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskPlan("do the thing", "~/.harness/plans/test.md")
	p.HandleKey(key("3"))     // highlight "Tell the model what to change"
	p.HandleKey(key("enter")) // open the feedback field
	for _, r := range "add tests" {
		p.HandleKey(key(string(r)))
	}
	p.HandleKey(key("enter")) // send
	decision := <-reply
	if decision.Kind != PlanRevise || decision.Feedback != "add tests" {
		t.Errorf("decision = %+v, want revise with feedback", decision)
	}
}

func TestPromptState_PlanEmptyFeedbackCancelsBackToMenu(t *testing.T) {
	p := NewPromptState("/tmp")
	p.AskPlan("do the thing", "~/.harness/plans/test.md")
	p.HandleKey(key("3"))     // highlight option 3
	p.HandleKey(key("enter")) // open the feedback field
	p.HandleKey(key("enter")) // empty feedback: back to the menu, not a revise
	if p.plan == nil {
		t.Fatal("plan prompt closed on empty feedback; want back to the menu")
	}
	if p.feedback != nil {
		t.Error("still in feedback mode after an empty enter")
	}
}

func backspaceKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyBackspace} }
