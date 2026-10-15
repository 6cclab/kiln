package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

// TestExitPlanModeAfterEnteringPlanMidSession wires the real controller to
// a real gate, as chat.go does: a session that starts in manual and moves
// the gate to plan later (/plan, Shift+Tab) gets the approver when the
// model presents its plan, and approving leaves plan mode on the gate.
// It used to answer "Not in plan mode; no approval needed. Proceed
// directly." because the controller kept its own startup copy of the mode.
func TestExitPlanModeAfterEnteringPlanMidSession(t *testing.T) {
	gate := permission.NewGate(permission.GateOptions{Mode: settings.ModeManual})
	c := agent.NewPlanController(func() bool { return gate.Mode() == settings.ModePlan })
	c.OnApprove = func(mode string) { gate.SetMode(settings.PermissionMode(mode)) }
	var asked int
	exit := tools.ExitPlanModeTool(c, func(context.Context, string) (tools.PlanDecision, error) {
		asked++
		return tools.PlanDecision{Kind: tools.PlanDecisionApprove, Mode: "acceptEdits"}, nil
	})
	run := func() string {
		res, err := exit.Execute(context.Background(), []byte(`{"plan":"1. Fix add()"}`), func(tool.Result) {}, tool.Invocation{ToolName: exit.Name})
		if err != nil {
			t.Fatal(err)
		}
		return msg.TextOf(res.Content)
	}

	gate.SetMode(settings.ModePlan)
	if got := run(); asked != 1 || !strings.Contains(got, "Plan approved") {
		t.Fatalf("first plan: asked=%d, result %q", asked, got)
	}
	if gate.Mode() != settings.ModeAcceptEdits {
		t.Fatalf("gate mode after approval = %q, want acceptEdits", gate.Mode())
	}
	gate.SetMode(settings.ModePlan) // a second plan in the same session
	if got := run(); asked != 2 || !strings.Contains(got, "Plan approved") {
		t.Fatalf("second plan: asked=%d, result %q", asked, got)
	}
}
