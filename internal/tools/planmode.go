package tools

// `exit_plan_mode` — the model's request to stop planning and start doing.
// A Go port of harness/src/agent/plan-mode.ts's createExitPlanModeTool.
//
// Deliberately a tool rather than a convention like "print PLAN:". A tool
// call is structured, unambiguous, and can block on a real answer; prose
// has to be pattern-matched and can be produced accidentally mid-thought.
//
// Deviation from the phase brief: ExitPlanModeTool does not take
// *agent.PlanController/agent.PlanApprover directly. internal/agent
// already imports internal/tools (session.go calls tools.Builtins), so
// internal/tools importing internal/agent back would be an import cycle.
// PlanModeState/PlanApprover/PlanDecision below are local mirrors of
// agent.PlanController/agent.PlanApprover/agent.PlanDecision;
// *agent.PlanController satisfies PlanModeState structurally (same method
// set), so wiring code (cli, a later phase) can pass it straight through
// with no adapter.
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/tool"
)

// PlanModeState reports whether the session is currently in plan mode, and
// is told when a plan is approved so it can leave read-only enforcement.
// *agent.PlanController implements this.
type PlanModeState interface {
	IsActive() bool
	Approve(mode string)
}

// PlanDecisionKind mirrors agent.PlanDecisionKind.
type PlanDecisionKind string

const (
	PlanDecisionApprove PlanDecisionKind = "approve"
	PlanDecisionRevise  PlanDecisionKind = "revise"
)

// PlanDecision mirrors agent.PlanDecision.
type PlanDecision struct {
	Kind     PlanDecisionKind
	Mode     string
	Feedback string
}

// PlanApprover asks the user to approve a plan. Implemented by the TUI.
type PlanApprover func(ctx context.Context, plan string) (PlanDecision, error)

var exitPlanModeParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"plan": {"type": "string", "description": "The plan, as markdown. Be specific about what will change."}
	},
	"required": ["plan"]
}`)

type exitPlanModeArgs struct {
	Plan string `json:"plan"`
}

// ExitPlanModeTool builds `exit_plan_mode`.
func ExitPlanModeTool(state PlanModeState, approve PlanApprover) *tool.Tool {
	return &tool.Tool{
		Name:  "exit_plan_mode",
		Label: "Present plan",
		Description: "Call this when you have finished researching and have a plan ready for the user to approve. " +
			"Only for tasks that will change things - if the user asked a question or wants research, " +
			"just answer. Pass the plan as markdown with three parts: the context (what is wrong or " +
			"needed, and why), the changes (each with its file paths), and how you will verify them " +
			"(the tests to add or run, and for a fix, a test that fails without it). Keep it easy to scan.",
		Parameters: exitPlanModeParameters,
		Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var a exitPlanModeArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return tool.Result{}, fmt.Errorf("exit_plan_mode: decoding arguments: %w", err)
				}
			}
			plan := strings.TrimSpace(a.Plan)

			if !state.IsActive() {
				// Calling it outside plan mode is a model error, not a
				// user-facing one. Say so plainly so it stops trying.
				return tool.Text("Not in plan mode; no approval needed. Proceed directly."), nil
			}
			if plan == "" {
				return tool.Text("No plan provided. Call again with the plan text."), nil
			}

			decision, err := approve(ctx, plan)
			if err != nil {
				return tool.Result{}, err
			}

			if decision.Kind == PlanDecisionApprove {
				state.Approve(decision.Mode)
				return tool.Text(fmt.Sprintf(
					"Plan approved. You may now make changes. Permission mode is %q. Follow the plan you presented.",
					decision.Mode,
				)), nil
			}

			// Stays in plan mode: the point of "revise" is another round
			// of planning, not a grudging approval.
			return tool.Text(fmt.Sprintf(
				"The user wants changes to the plan before proceeding: %s\n\nStill in plan mode - research and present a revised plan.",
				decision.Feedback,
			)), nil
		},
	}
}
