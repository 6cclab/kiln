package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Key handling here mirrors permission-prompt.ts:97-189 exactly (see
// permissionview.go's doc comment); these tests exercise that contract
// directly against PromptState, without a PTY.

func TestPromptState_ToolAllow(t *testing.T) {
	for _, k := range []string{"1", "y", "enter"} {
		p := NewPromptState("/tmp")
		reply := p.AskTool(PermissionRequest{ToolName: "bash"})
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
		reply := p.AskTool(PermissionRequest{ToolName: "bash"})
		p.HandleKey(key(k))
		if choice := <-reply; choice.Kind != ChoiceAllowAlways {
			t.Errorf("key %q: kind = %q, want allow-always", k, choice.Kind)
		}
	}
}

func TestPromptState_ToolDenyWithFeedback(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{ToolName: "bash"})

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
	reply := p.AskTool(PermissionRequest{ToolName: "bash"})
	p.HandleKey(key("esc"))
	choice := <-reply
	if choice.Kind != ChoiceDeny || choice.Feedback != "" {
		t.Errorf("choice = %+v, want a bare deny", choice)
	}
}

func TestPromptState_FeedbackBackspace(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{ToolName: "bash"})
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
	p.AskTool(PermissionRequest{ToolName: "bash"})
	if !p.HandleKey(key("z")) {
		t.Error("unknown key not swallowed while a prompt is active")
	}
	if !p.Active() {
		t.Error("prompt closed by an unrelated key")
	}
}

func TestPromptState_PlanApproveAcceptEdits(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskPlan("do the thing")
	p.HandleKey(key("1"))
	decision := <-reply
	if decision.Kind != PlanApprove || decision.Mode != "acceptEdits" {
		t.Errorf("decision = %+v, want approve/acceptEdits", decision)
	}
}

func TestPromptState_PlanApproveManual(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskPlan("do the thing")
	p.HandleKey(key("2"))
	decision := <-reply
	if decision.Kind != PlanApprove || decision.Mode != "manual" {
		t.Errorf("decision = %+v, want approve/manual", decision)
	}
}

func TestPromptState_PlanReviseWithFeedback(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskPlan("do the thing")
	p.HandleKey(key("3"))
	for _, r := range "add tests" {
		p.HandleKey(key(string(r)))
	}
	p.HandleKey(key("enter"))
	decision := <-reply
	if decision.Kind != PlanRevise || decision.Feedback != "add tests" {
		t.Errorf("decision = %+v, want revise with feedback", decision)
	}
}

func TestPromptState_PlanEmptyFeedbackCancelsBackToMenu(t *testing.T) {
	p := NewPromptState("/tmp")
	p.AskPlan("do the thing")
	p.HandleKey(key("3"))
	p.HandleKey(key("enter")) // empty feedback: back to the menu, not a revise
	if p.plan == nil {
		t.Fatal("plan prompt closed on empty feedback; want back to the menu")
	}
	if p.feedback != nil {
		t.Error("still in feedback mode after an empty enter")
	}
}

func backspaceKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyBackspace} }
