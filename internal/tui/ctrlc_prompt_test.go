package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/tools"
)

// ctrlCMsg is the KeyPressMsg bubbletea reports for Ctrl+C, matching the
// other key-press literals this package's tests already build by hand
// (e.g. app_test.go's charKey, TestRouter_EscInterruptsOnlyWhileBusy's
// tea.KeyPressMsg{Code: tea.KeyEscape}).
var ctrlCMsg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

// recvChoice drains reply without blocking the test forever: a prompt
// Ctrl+C never answered leaves its caller (the permission gate, the plan
// approver, the question approver) blocked for good, which is exactly the
// defect this file pins.
func recvChoice(t interface {
	Helper()
	Fatalf(string, ...any)
}, reply chan PromptChoice) PromptChoice {
	select {
	case c := <-reply:
		return c
	case <-time.After(time.Second):
		t.Fatalf("reply channel never received a value: Ctrl+C left the caller blocked")
		return PromptChoice{}
	}
}

func recvPlan(t interface {
	Helper()
	Fatalf(string, ...any)
}, reply chan PlanReply) PlanReply {
	select {
	case r := <-reply:
		return r
	case <-time.After(time.Second):
		t.Fatalf("plan reply channel never received a value: Ctrl+C left the caller blocked")
		return PlanReply{}
	}
}

func recvAskUser(t interface {
	Helper()
	Fatalf(string, ...any)
}, reply chan AskUserReply) AskUserReply {
	select {
	case r := <-reply:
		return r
	case <-time.After(time.Second):
		t.Fatalf("question reply channel never received a value: Ctrl+C left the caller blocked")
		return AskUserReply{}
	}
}

// TestCtrlC_PermissionPrompt_DeclinesAndInterrupts pins the reported
// defect: Ctrl+C while a tool-permission prompt is up must decline it (so
// the gate's blocked caller is released) and interrupt the running turn
// in one key press, as in Claude Code. Before the fix,
// PromptState.HandleKey's default case swallows every unmatched key
// (including Ctrl+C) and returns true, so the reply channel above is
// never written and this test times out.
func TestCtrlC_PermissionPrompt_DeclinesAndInterrupts(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	m.busy = true
	m.cfg.Lane = &harness.Lane{}
	reply := make(chan PromptChoice, 1)
	m.prompt.pending = &pendingPermission{
		request: PermissionRequest{ToolName: "bash", PrimaryArg: "npm test -- upload", Grantable: true},
		reply:   reply,
	}

	next, _ := m.handleKey(ctrlCMsg)
	nm := next.(Model)

	choice := recvChoice(t, reply)
	if choice.Kind != ChoiceDeny {
		t.Errorf("choice.Kind = %q, want %q", choice.Kind, ChoiceDeny)
	}
	if nm.prompt.Active() {
		t.Errorf("prompt still active after Ctrl+C")
	}
	if !nm.busy {
		t.Errorf("busy cleared synchronously by Ctrl+C; only the harness reporting the abort should clear it")
	}
	_ = f
}

// TestCtrlC_DuringDenyFeedback_StillDeclinesAndInterrupts: Ctrl+C typed
// while composing a deny reason must not be swallowed as ordinary text
// input: as in Claude Code, it declines and interrupts like Ctrl+C at
// the prompt itself, discarding the partial text.
func TestCtrlC_DuringDenyFeedback_StillDeclinesAndInterrupts(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.busy = true
	m.cfg.Lane = &harness.Lane{}
	reply := make(chan PromptChoice, 1)
	m.prompt.pending = &pendingPermission{
		request: PermissionRequest{ToolName: "fetch", PrimaryArg: "http://example.com"},
		reply:   reply,
	}
	fb := "partial reason"
	m.prompt.feedback = &fb

	next, _ := m.handleKey(ctrlCMsg)
	nm := next.(Model)

	choice := recvChoice(t, reply)
	if choice.Kind != ChoiceDeny {
		t.Errorf("choice.Kind = %q, want %q", choice.Kind, ChoiceDeny)
	}
	if nm.prompt.Active() {
		t.Errorf("prompt still active after Ctrl+C during feedback entry")
	}
}

// TestCtrlC_PlanPrompt_DeclinesAndInterrupts covers the plan-approval
// prompt: Ctrl+C must revise/cancel it, not fall through to "tell kiln
// what to change" (that is Esc/n's job, per handlePlanKey) and not sit
// silently swallowed.
func TestCtrlC_PlanPrompt_DeclinesAndInterrupts(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.busy = true
	m.cfg.Lane = &harness.Lane{}
	reply := make(chan PlanReply, 1)
	m.prompt.plan = &pendingPlan{plan: "do the thing", reply: reply}

	next, _ := m.handleKey(ctrlCMsg)
	nm := next.(Model)

	got := recvPlan(t, reply)
	if got.Kind != PlanRevise {
		t.Errorf("plan reply.Kind = %q, want %q", got.Kind, PlanRevise)
	}
	if nm.prompt.Active() {
		t.Errorf("plan prompt still active after Ctrl+C")
	}
}

// TestCtrlC_QuestionPrompt_DeclinesAndInterrupts covers ask_user_question.
func TestCtrlC_QuestionPrompt_DeclinesAndInterrupts(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.busy = true
	m.cfg.Lane = &harness.Lane{}
	reply := make(chan AskUserReply, 1)
	m.prompt.question = &pendingQuestion{
		questions: []tools.AskUserQuestion{{Header: "h", Question: "q?", Options: []tools.AskUserOption{{Label: "a"}}}},
		checked:   map[int]bool{},
		reply:     reply,
	}

	next, _ := m.handleKey(ctrlCMsg)
	nm := next.(Model)

	got := recvAskUser(t, reply)
	if !got.Cancelled {
		t.Errorf("question reply.Cancelled = false, want true")
	}
	if nm.prompt.Active() {
		t.Errorf("question prompt still active after Ctrl+C")
	}
}

// TestCtrlC_AnswersQueuedPromptsToo: a turn stopped by Ctrl+C must answer
// every prompt still waiting behind the one on screen (cancelAll's job),
// not just the one the user was looking at — concurrent subagents each
// raising their own bash approval must not leave a caller blocked.
func TestCtrlC_AnswersQueuedPromptsToo(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.busy = true
	m.cfg.Lane = &harness.Lane{}
	onScreen := make(chan PromptChoice, 1)
	queued := make(chan PromptChoice, 1)
	m.prompt.pending = &pendingPermission{
		request: PermissionRequest{ToolName: "bash", PrimaryArg: "one"},
		reply:   onScreen,
	}
	m.prompt.queued = []queuedPrompt{{perm: &pendingPermission{
		request: PermissionRequest{ToolName: "bash", PrimaryArg: "two"},
		reply:   queued,
	}}}

	next, _ := m.handleKey(ctrlCMsg)
	nm := next.(Model)

	if c := recvChoice(t, onScreen); c.Kind != ChoiceDeny {
		t.Errorf("on-screen prompt choice = %q, want %q", c.Kind, ChoiceDeny)
	}
	if c := recvChoice(t, queued); c.Kind != ChoiceDeny {
		t.Errorf("queued prompt choice = %q, want %q", c.Kind, ChoiceDeny)
	}
	if nm.prompt.Active() || len(nm.prompt.queued) != 0 {
		t.Errorf("prompt state not fully cleared after Ctrl+C: active=%v queued=%d", nm.prompt.Active(), len(nm.prompt.queued))
	}
}

// TestCtrlC_AtPrompt_DoesNotArmDoublePressToExit: as in Claude Code, a
// Ctrl+C that answers a prompt does not count toward a double Ctrl+C
// exit. A later Ctrl+C once idle starts that sequence fresh rather than
// exiting as if the prompt's Ctrl+C had been the first half.
func TestCtrlC_AtPrompt_DoesNotArmDoublePressToExit(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.busy = true
	m.cfg.Lane = &harness.Lane{}
	reply := make(chan PromptChoice, 1)
	m.prompt.pending = &pendingPermission{
		request: PermissionRequest{ToolName: "bash", PrimaryArg: "one"},
		reply:   reply,
	}

	next, _ := m.handleKey(ctrlCMsg)
	nm := next.(Model)
	recvChoice(t, reply)

	// Immediately after, with the prompt gone and the turn no longer
	// busy, a fresh Ctrl+C must NOT exit on its own — it would if the
	// prompt's Ctrl+C had armed the double-press window.
	nm.busy = false
	after, cmd := nm.handleKey(ctrlCMsg)
	_ = after.(Model)
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Errorf("single post-prompt Ctrl+C quit the app; the prompt's own Ctrl+C must not have armed the double-press window")
		}
	}
}
