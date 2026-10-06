package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/tools"
)

// Prompts reach the model from more than one goroutine: the permission
// gate serialises its own prompts, but the plan and ask_user_question
// approvers do not go through it, and a sandboxed background shell asks
// for network access whenever it connects, whatever the lane is doing. A
// prompt that arrives while another is up waits its turn and shows once
// the one on screen is answered. None is ever dropped without a reply: a
// dropped reply leaves its tool call blocked for good, the turn busy with
// no dialog on screen.

func bashPrompt(reply chan PromptChoice) MsgPermissionPrompt {
	return MsgPermissionPrompt{
		Request: PermissionRequest{ToolName: "bash", PrimaryArg: "npm test", Grantable: true},
		Reply:   reply,
	}
}

func oneQuestion(reply chan AskUserReply) MsgAskUserPrompt {
	return MsgAskUserPrompt{
		Questions: []tools.AskUserQuestion{{
			Question: "Which?",
			Header:   "Pick",
			Options:  []tools.AskUserOption{{Label: "A"}, {Label: "B"}},
		}},
		Reply: reply,
	}
}

func press(t *testing.T, m Model, key string) Model {
	t.Helper()
	if key == "esc" {
		return updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	}
	r := []rune(key)[0]
	return updateModel(t, m, tea.KeyPressMsg{Code: r, Text: key})
}

func noReply[T any](t *testing.T, what string, ch chan T) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("%s answered too early with %+v", what, v)
	default:
	}
}

func gotReply[T any](t *testing.T, what string, ch chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	default:
		t.Fatalf("%s was never answered: its caller would block forever", what)
	}
	var zero T
	return zero
}

func TestPromptQueue_PlanWhilePermissionUp(t *testing.T) {
	m := busyModelRunning(t, "Running bash")
	perm := make(chan PromptChoice, 1)
	plan := make(chan PlanReply, 1)
	m = updateModel(t, m, bashPrompt(perm))
	m = updateModel(t, m, MsgPlanPrompt{Plan: "1. do it", Reply: plan})

	if m.prompt.pending == nil || m.prompt.plan != nil {
		t.Fatal("the plan prompt replaced the permission prompt on screen; it must wait its turn")
	}
	if got := m.spinner.Label(); got != "Waiting for approval" {
		t.Fatalf("label with two prompts waiting = %q", got)
	}
	m = press(t, m, "1")
	if c := gotReply(t, "the permission prompt", perm); c.Kind != ChoiceAllow {
		t.Fatalf("permission reply = %v, want allow", c.Kind)
	}
	if m.prompt.plan == nil {
		t.Fatal("the queued plan prompt did not show once the permission prompt was answered")
	}
	if got := m.spinner.Label(); got != "Waiting for approval" {
		t.Errorf("label while the queued plan prompt is up = %q, want %q", got, "Waiting for approval")
	}
	noReply(t, "the plan prompt", plan)
	m = press(t, m, "2")
	if r := gotReply(t, "the plan prompt", plan); r.Kind != PlanApprove {
		t.Fatalf("plan reply = %v, want approve", r.Kind)
	}
	if m.prompt.Active() {
		t.Fatal("a prompt is still up after both were answered")
	}
	if got := m.spinner.Label(); got != "Running bash" {
		t.Errorf("label after the last prompt = %q, want the one saved before the first, %q", got, "Running bash")
	}
}

func TestPromptQueue_QuestionWhilePermissionUp(t *testing.T) {
	m := busyModelRunning(t, "Running bash")
	perm := make(chan PromptChoice, 1)
	ask := make(chan AskUserReply, 1)
	m = updateModel(t, m, bashPrompt(perm))
	m = updateModel(t, m, oneQuestion(ask))

	if m.prompt.pending == nil || m.prompt.question != nil {
		t.Fatal("the question replaced the permission prompt on screen; it must wait its turn")
	}
	m = press(t, m, "1")
	if c := gotReply(t, "the permission prompt", perm); c.Kind != ChoiceAllow {
		t.Fatalf("permission reply = %v, want allow", c.Kind)
	}
	if m.prompt.question == nil {
		t.Fatal("the queued question did not show once the permission prompt was answered")
	}
	if got := m.spinner.Label(); got != "Waiting for an answer" {
		t.Errorf("label while the queued question is up = %q, want %q", got, "Waiting for an answer")
	}
	m = press(t, m, "2")
	if r := gotReply(t, "the question", ask); r.Cancelled || len(r.Answers) != 1 || r.Answers[0].Answers[0] != "B" {
		t.Fatalf("question reply = %+v, want answer B", r)
	}
	if m.prompt.Active() {
		t.Fatal("a prompt is still up after both were answered")
	}
	if got := m.spinner.Label(); got != "Running bash" {
		t.Errorf("label after the last prompt = %q, want %q", got, "Running bash")
	}
}

func TestPromptQueue_PermissionWhilePlanUp(t *testing.T) {
	m := busyModelRunning(t, "Running exit_plan_mode")
	plan := make(chan PlanReply, 1)
	perm := make(chan PromptChoice, 1)
	m = updateModel(t, m, MsgPlanPrompt{Plan: "1. do it", Reply: plan})
	m = updateModel(t, m, bashPrompt(perm))

	if m.prompt.plan == nil || m.prompt.pending != nil {
		t.Fatal("the permission prompt replaced the plan prompt on screen; it must wait its turn")
	}
	m = press(t, m, "2")
	if r := gotReply(t, "the plan prompt", plan); r.Kind != PlanApprove {
		t.Fatalf("plan reply = %v, want approve", r.Kind)
	}
	if m.prompt.pending == nil {
		t.Fatal("the queued permission prompt did not show once the plan was answered")
	}
	m = press(t, m, "1")
	if c := gotReply(t, "the permission prompt", perm); c.Kind != ChoiceAllow {
		t.Fatalf("permission reply = %v, want allow", c.Kind)
	}
	if m.prompt.Active() {
		t.Fatal("a prompt is still up after both were answered")
	}
}

func TestPromptQueue_PermissionWhileQuestionUp(t *testing.T) {
	m := busyModelRunning(t, "Running ask_user_question")
	ask := make(chan AskUserReply, 1)
	perm := make(chan PromptChoice, 1)
	m = updateModel(t, m, oneQuestion(ask))
	m = updateModel(t, m, bashPrompt(perm))

	if m.prompt.question == nil || m.prompt.pending != nil {
		t.Fatal("two prompts are armed at once; the permission prompt must wait its turn")
	}
	if got := m.spinner.Label(); got != "Waiting for an answer" {
		t.Errorf("label with the question on screen = %q, want %q", got, "Waiting for an answer")
	}
	m = press(t, m, "1")
	if r := gotReply(t, "the question", ask); r.Cancelled {
		t.Fatalf("question reply = %+v, want an answer", r)
	}
	if m.prompt.pending == nil {
		t.Fatal("the queued permission prompt did not show once the question was answered")
	}
	if got := m.spinner.Label(); got != "Waiting for approval" {
		t.Errorf("label while the queued permission prompt is up = %q, want %q", got, "Waiting for approval")
	}
	m = press(t, m, "1")
	gotReply(t, "the permission prompt", perm)
}

func TestPromptQueue_TwoPermissionPromptsFIFO(t *testing.T) {
	m := busyModelRunning(t, "Running bash")
	first := make(chan PromptChoice, 1)
	second := make(chan PromptChoice, 1)
	m = updateModel(t, m, bashPrompt(first))
	m = updateModel(t, m, MsgPermissionPrompt{
		Request: PermissionRequest{ToolName: permission.NetworkToolName, PrimaryArg: "example.com:443"},
		Reply:   second,
	})
	if m.prompt.pending == nil || m.prompt.pending.reply != first {
		t.Fatal("the second permission prompt replaced the first on screen")
	}
	m = press(t, m, "1")
	gotReply(t, "the first permission prompt", first)
	noReply(t, "the second permission prompt", second)
	if m.prompt.pending == nil || m.prompt.pending.reply != second {
		t.Fatal("the second permission prompt did not show after the first was answered")
	}
	m = press(t, m, "esc")
	if c := gotReply(t, "the second permission prompt", second); c.Kind != ChoiceDeny {
		t.Fatalf("second reply = %v, want deny", c.Kind)
	}
	if m.prompt.Active() {
		t.Fatal("a prompt is still up after both were answered")
	}
}

// Esc on a tool-permission prompt while a turn runs declines it and stops
// the turn; every prompt still waiting behind it is answered too (denied
// or cancelled), so none of their callers stays blocked.
func TestPromptQueue_EscInterruptAnswersEveryQueuedPrompt(t *testing.T) {
	m := busyModelRunning(t, "Running bash")
	perm := make(chan PromptChoice, 1)
	perm2 := make(chan PromptChoice, 1)
	plan := make(chan PlanReply, 1)
	ask := make(chan AskUserReply, 1)
	m = updateModel(t, m, bashPrompt(perm))
	m = updateModel(t, m, MsgPlanPrompt{Plan: "1. do it", Reply: plan})
	m = updateModel(t, m, oneQuestion(ask))
	m = updateModel(t, m, bashPrompt(perm2))

	m = press(t, m, "esc")

	if c := gotReply(t, "the permission prompt on screen", perm); c.Kind != ChoiceDeny {
		t.Fatalf("on-screen permission reply = %v, want deny", c.Kind)
	}
	if r := gotReply(t, "the queued plan prompt", plan); r.Kind != PlanRevise {
		t.Fatalf("queued plan reply = %v, want revise (not approved)", r.Kind)
	}
	if r := gotReply(t, "the queued question", ask); !r.Cancelled {
		t.Fatalf("queued question reply = %+v, want cancelled", r)
	}
	if c := gotReply(t, "the queued permission prompt", perm2); c.Kind != ChoiceDeny {
		t.Fatalf("queued permission reply = %v, want deny", c.Kind)
	}
	if m.prompt.Active() {
		t.Fatal("a prompt is still up after Esc stopped the turn")
	}
}
