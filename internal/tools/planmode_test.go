package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// fakePlanState is a minimal PlanModeState for tests, and also proves
// *agent.PlanController would satisfy this interface structurally (same
// method set: IsActive() bool, Approve(string)) without either package
// importing the other.
type fakePlanState struct {
	active       bool
	approvedMode string
	approveCalls int
}

func (f *fakePlanState) IsActive() bool { return f.active }
func (f *fakePlanState) Approve(mode string) {
	f.approveCalls++
	f.approvedMode = mode
	f.active = false
}

func textOfBlocks(res tool.Result) string {
	return msg.TextOf(res.Content)
}

func TestExitPlanModeNotActive(t *testing.T) {
	state := &fakePlanState{active: false}
	tl := ExitPlanModeTool(state, func(ctx context.Context, plan string) (PlanDecision, error) {
		t.Fatal("approve should not be called when not in plan mode")
		return PlanDecision{}, nil
	})

	res := execTool(t, tl, map[string]any{"plan": "do the thing"})
	text := textOfBlocks(res)
	if !strings.Contains(text, "Not in plan mode") {
		t.Fatalf("text = %q, want a not-in-plan-mode message", text)
	}
}

func TestExitPlanModeNoPlan(t *testing.T) {
	state := &fakePlanState{active: true}
	tl := ExitPlanModeTool(state, func(ctx context.Context, plan string) (PlanDecision, error) {
		t.Fatal("approve should not be called with an empty plan")
		return PlanDecision{}, nil
	})

	res := execTool(t, tl, map[string]any{"plan": "   "})
	text := textOfBlocks(res)
	if !strings.Contains(text, "No plan provided") {
		t.Fatalf("text = %q, want a no-plan message", text)
	}
}

func TestExitPlanModeApprove(t *testing.T) {
	state := &fakePlanState{active: true}
	tl := ExitPlanModeTool(state, func(ctx context.Context, plan string) (PlanDecision, error) {
		if plan != "read files, then edit them" {
			t.Fatalf("approve saw plan %q", plan)
		}
		return PlanDecision{Kind: PlanDecisionApprove, Mode: "acceptEdits"}, nil
	})

	res := execTool(t, tl, map[string]any{"plan": "read files, then edit them"})
	text := textOfBlocks(res)
	if !strings.Contains(text, "Plan approved") || !strings.Contains(text, "acceptEdits") {
		t.Fatalf("text = %q, want an approval message naming the mode", text)
	}
	if state.active {
		t.Fatal("state was not told to leave plan mode")
	}
	if state.approveCalls != 1 || state.approvedMode != "acceptEdits" {
		t.Fatalf("state.Approve called with %d calls, mode %q", state.approveCalls, state.approvedMode)
	}
}

func TestExitPlanModeRevise(t *testing.T) {
	state := &fakePlanState{active: true}
	tl := ExitPlanModeTool(state, func(ctx context.Context, plan string) (PlanDecision, error) {
		return PlanDecision{Kind: PlanDecisionRevise, Feedback: "use the staging bucket"}, nil
	})

	res := execTool(t, tl, map[string]any{"plan": "delete production"})
	text := textOfBlocks(res)
	if !strings.Contains(text, "use the staging bucket") {
		t.Fatalf("text = %q, want the feedback verbatim", text)
	}
	if !strings.Contains(text, "Still in plan mode") {
		t.Fatalf("text = %q, want it to say plan mode continues", text)
	}
	if !state.active {
		t.Fatal("revise must not leave plan mode")
	}
	if state.approveCalls != 0 {
		t.Fatal("revise must not call Approve")
	}
}
