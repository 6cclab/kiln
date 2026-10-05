package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The busy row says "Waiting for approval" only while a prompt is up.
// Answering it hands the row back: to what it said before the prompt
// ("Running bash", set on EventToolStart) when the call goes ahead, or to
// the turn's gerund when it is declined. Before, the row kept saying
// "Waiting for approval" through the whole tool run and the model's next
// request, until the first streamed text reset it, so an answered prompt
// looked like one still waiting for its answer.

func busyModelRunning(t *testing.T, label string) Model {
	t.Helper()
	m := newTestModel()
	m.busy = true
	m.spinner.Start(0)
	return updateModel(t, m, MsgSpinnerLabel{Text: label})
}

func updateModel(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	mi, _ := m.Update(msg)
	return mi.(Model)
}

func TestPromptAnswered_AllowRestoresTheToolLabel(t *testing.T) {
	m := busyModelRunning(t, "Running bash")
	reply := make(chan PromptChoice, 1)
	m = updateModel(t, m, MsgPermissionPrompt{
		Request: PermissionRequest{ToolName: "bash", PrimaryArg: "npm test -- upload", Grantable: true},
		Reply:   reply,
	})
	if got := m.spinner.Label(); got != "Waiting for approval" {
		t.Fatalf("label while the prompt is up = %q, want %q", got, "Waiting for approval")
	}

	m = updateModel(t, m, tea.KeyPressMsg{Code: '1', Text: "1"})

	select {
	case choice := <-reply:
		if choice.Kind != ChoiceAllow {
			t.Fatalf("reply = %v, want ChoiceAllow", choice.Kind)
		}
	default:
		t.Fatal("pressing 1 did not answer the prompt")
	}
	if m.prompt.Active() {
		t.Fatal("prompt still active after it was answered")
	}
	if got := m.spinner.Label(); got != "Running bash" {
		t.Errorf("label after approving = %q, want %q", got, "Running bash")
	}
}

func TestPromptAnswered_DeclineResetsToTheGerund(t *testing.T) {
	m := busyModelRunning(t, "Running bash")
	gerund := PickLabel(0)
	reply := make(chan PromptChoice, 1)
	m = updateModel(t, m, MsgPermissionPrompt{
		Request: PermissionRequest{ToolName: "bash", PrimaryArg: "npm test -- upload", Grantable: true},
		Reply:   reply,
	})

	m = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	select {
	case choice := <-reply:
		if choice.Kind != ChoiceDeny {
			t.Fatalf("reply = %v, want ChoiceDeny", choice.Kind)
		}
	default:
		t.Fatal("esc did not answer the prompt")
	}
	if got := m.spinner.Label(); got != gerund {
		t.Errorf("label after declining = %q, want the turn's gerund %q", got, gerund)
	}
}

func TestPromptAnswered_PlanApprovalRestoresTheToolLabel(t *testing.T) {
	m := busyModelRunning(t, "Running exit_plan_mode")
	reply := make(chan PlanReply, 1)
	m = updateModel(t, m, MsgPlanPrompt{Plan: "1. do it", Reply: reply})
	if got := m.spinner.Label(); got != "Waiting for approval" {
		t.Fatalf("label while the plan prompt is up = %q, want %q", got, "Waiting for approval")
	}

	m = updateModel(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})

	select {
	case r := <-reply:
		if r.Kind != PlanApprove {
			t.Fatalf("reply = %v, want PlanApprove", r.Kind)
		}
	default:
		t.Fatal("pressing 2 did not answer the plan prompt")
	}
	if got := m.spinner.Label(); got != "Running exit_plan_mode" {
		t.Errorf("label after approving the plan = %q, want %q", got, "Running exit_plan_mode")
	}
}
