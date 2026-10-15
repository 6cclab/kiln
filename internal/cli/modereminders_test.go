package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
)

func remindersAt(mode *claudesettings.PermissionMode) *modeReminders {
	return newModeReminders(func() claudesettings.PermissionMode { return *mode }, "")
}

func TestModeReminders_EnterPlanMidSessionFullThenSparse(t *testing.T) {
	mode := claudesettings.ModeManual
	r := remindersAt(&mode)
	if got := r.TakeForPrompt(); got != "" {
		t.Fatalf("manual session got a reminder: %q", got)
	}
	mode = claudesettings.ModePlan
	if got := r.TakeForPrompt(); !strings.Contains(got, "You are in PLAN MODE.") || !strings.HasPrefix(got, "<system-reminder>") {
		t.Fatalf("entering plan: %q, want the full plan-mode text in a system-reminder", got)
	}
	if got := r.TakeForPrompt(); !strings.Contains(got, agent.PlanModeSparseReminder) || strings.Contains(got, "You are in PLAN MODE.") {
		t.Fatalf("still in plan: %q, want only the sparse reminder", got)
	}
}

func TestModeReminders_ShiftTabOutOfPlanSendsExit(t *testing.T) {
	mode := claudesettings.ModePlan
	r := remindersAt(&mode)
	r.TakeForPrompt()
	mode = claudesettings.ModeAuto
	if got := r.TakeForPrompt(); !strings.Contains(got, "left plan mode") || !strings.Contains(got, `"auto"`) {
		t.Fatalf("leaving plan: %q, want the exit reminder naming auto", got)
	}
	if got := r.TakeForPrompt(); got != "" {
		t.Fatalf("exit reminder repeated: %q", got)
	}
}

func TestModeReminders_ApprovalSuppressesExitReminder(t *testing.T) {
	mode := claudesettings.ModePlan
	r := remindersAt(&mode)
	r.TakeForPrompt()
	mode = claudesettings.ModeAcceptEdits
	r.LeftPlanByApproval()
	if got := r.TakeForPrompt(); got != "" {
		t.Fatalf("after approval: %q, want nothing (the tool result said it)", got)
	}
}

func TestModeReminders_TransformContextUntilPersisted(t *testing.T) {
	mode := claudesettings.ModeManual
	r := remindersAt(&mode)
	r.TakeForPrompt()
	base := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}

	out, _ := r.TransformContext(context.Background(), base)
	if len(out) != 1 {
		t.Fatalf("no mode change, yet TransformContext added %d messages", len(out)-1)
	}
	mode = claudesettings.ModePlan // Shift+Tab mid-turn
	for i := 0; i < 2; i++ {
		out, _ = r.TransformContext(context.Background(), base)
		if len(out) != 2 || !strings.Contains(msg.TextOf(out[1].(msg.UserMessage).Content), "You are in PLAN MODE.") {
			t.Fatalf("request %d after a mid-turn switch does not carry the plan text", i)
		}
	}
	if got := r.TakeForPrompt(); !strings.Contains(got, "You are in PLAN MODE.") {
		t.Fatalf("next prompt must store the full text the unstored one only showed: %q", got)
	}
	out, _ = r.TransformContext(context.Background(), base)
	if len(out) != 1 {
		t.Fatal("TransformContext kept adding the reminder after a prompt stored it")
	}
}

func TestModeReminders_ForgetResendsFullPlanText(t *testing.T) {
	mode := claudesettings.ModePlan
	r := remindersAt(&mode)
	r.TakeForPrompt()
	r.Forget()
	if got := r.TakeForPrompt(); !strings.Contains(got, "You are in PLAN MODE.") {
		t.Fatalf("after compaction: %q, want the full plan text again", got)
	}
}
