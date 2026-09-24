package agent

// Plan mode. A Go port of harness/src/agent/plan-mode.ts's data and
// control types; the tool itself (exit_plan_mode) lives in
// internal/tools/planmode.go — see that file's doc comment for why it
// does not import this package's types directly.
//
// Read-only enforcement is only half of it. Blocking mutations gives you a
// crippled session, not a planning session — what makes plan mode useful is
// the *handoff*: the model researches, presents a plan, and waits for
// approval before anything is allowed to change.

import (
	"context"
	"strings"
)

// PlanModePrompt is a verbatim port of plan-mode.ts's PLAN_MODE_PROMPT,
// appended to the system prompt while a session is in plan mode. Without
// it the model discovers it is read-only by hitting refusals one tool at a
// time, which reads as a broken session rather than a deliberate mode.
//
// internal/cli/chat.go currently carries its own unexported copy
// (planModePrompt) with a comment saying it should move here once this
// package existed. chat.go is being edited by another agent in this phase
// and is intentionally left untouched; see the phase report for the
// follow-up (chat.go should import agent.PlanModePrompt and delete its own
// copy).
var PlanModePrompt = strings.Join([]string{
	"You are in PLAN MODE. You may read files, search, and run read-only commands,",
	"but you must not edit, write, or run anything that changes state.",
	"",
	"Research the task thoroughly first. When you have a concrete plan, call",
	"exit_plan_mode with it and wait for approval. Do not attempt changes before",
	"the plan is approved - they will be refused.",
	"",
	"If the user only asked a question, answer it; do not present a plan.",
}, "\n")

// PlanDecisionKind discriminates PlanDecision.
type PlanDecisionKind string

const (
	// PlanDecisionApprove approves the plan and lets the model proceed,
	// leaving plan mode.
	PlanDecisionApprove PlanDecisionKind = "approve"
	// PlanDecisionRevise keeps planning; Feedback goes back to the model.
	PlanDecisionRevise PlanDecisionKind = "revise"
)

// PlanDecision is the user's answer to a presented plan.
type PlanDecision struct {
	Kind PlanDecisionKind
	// Mode is set when Kind == PlanDecisionApprove: the permission mode to
	// leave plan mode into (e.g. "acceptEdits", "manual"). A string here,
	// not claude/settings.PermissionMode, so this package does not need to
	// import claude/settings just to name it; callers pass the mode's
	// string value.
	Mode string
	// Feedback is set when Kind == PlanDecisionRevise.
	Feedback string
}

// PlanApprover asks the user to approve a plan. Implemented by the TUI.
type PlanApprover func(ctx context.Context, plan string) (PlanDecision, error)

// PlanController tracks whether a session is in plan mode and notifies
// OnApprove when a plan is approved, so the caller can leave read-only
// enforcement. Mirrors plan-mode.ts's PlanModeController.
type PlanController struct {
	active bool
	// OnApprove is called by Approve when a plan is approved, with the
	// mode to switch to.
	OnApprove func(mode string)
}

// NewPlanController returns a controller with plan mode active, matching
// how a session enters plan mode (--permission-mode plan, or Shift+Tab
// cycling into "plan").
func NewPlanController() *PlanController { return &PlanController{active: true} }

// IsActive reports whether the controller is currently in plan mode.
func (c *PlanController) IsActive() bool { return c.active }

// SetActive is exposed for callers that toggle plan mode outside of an
// approval, e.g. Shift+Tab cycling back into "plan".
func (c *PlanController) SetActive(active bool) { c.active = active }

// Approve leaves plan mode and calls OnApprove, if set, with mode.
func (c *PlanController) Approve(mode string) {
	c.active = false
	if c.OnApprove != nil {
		c.OnApprove(mode)
	}
}
