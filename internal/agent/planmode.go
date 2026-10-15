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

// PlanModePrompt is what the model is told on entering plan mode, sent as
// a reminder with the next prompt (internal/cli/modereminders.go), never in
// the system prompt: the mode changes mid-session and the system prompt
// must not, or every switch would cost the whole prompt cache. Without it
// the model discovers it is read-only by hitting refusals one tool at a
// time, which reads as a broken session rather than a deliberate mode.
//
// It no longer matches plan-mode.ts: the four-line original told the model
// only to research "thoroughly", and in the head-to-head against Claude Code
// (see BasePrompt) kiln's plans were faster but missed findings Claude Code's
// structured plan phase caught. The steps below give planning a shape:
// understand, check against the spec, sort findings, ask, then present a plan
// that says how it will be verified.
var PlanModePrompt = strings.Join([]string{
	"You are in PLAN MODE. You may read files, search, and run read-only commands,",
	"but you must not edit, write, or run anything that changes state. Changes",
	"attempted before the plan is approved will be refused.",
	"",
	"If the user only asked a question, answer it; do not present a plan.",
	"",
	"Otherwise, plan in this order:",
	"1. Understand. Read the request, the code it touches and any spec, README or",
	"   issue it refers to, in full. For a large or unfamiliar codebase, use task",
	"   subagents to explore areas in parallel.",
	"2. Check. Go through the spec or requirements rule by rule and compare each with",
	"   what the code does, edge cases and error paths included. Look for existing",
	"   code to reuse before proposing new code.",
	"3. Sort. Split what you found into changes the task requires and optional ones.",
	"   If the scope is unclear or a choice belongs to the user, ask with",
	"   ask_user_question before you finish the plan.",
	"4. Present. Call exit_plan_mode with the plan and wait for approval.",
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

// PlanController answers whether a session is in plan mode and notifies
// OnApprove when a plan is approved, so the caller can leave read-only
// enforcement. Mirrors plan-mode.ts's PlanModeController, except that it
// keeps no state of its own: plan mode is the permission gate's mode, and
// the gate changes from many places (Shift+Tab, /plan, /permissions, a
// prompt's mode switch). A copy of it went stale the first time any of
// them moved the gate, and exit_plan_mode then told a planning model to
// "proceed directly".
type PlanController struct {
	isActive func() bool
	// OnApprove is called by Approve when a plan is approved, with the
	// mode to switch to. It is what leaves plan mode.
	OnApprove func(mode string)
}

// NewPlanController returns a controller whose plan state is isActive,
// normally "the gate's mode is plan".
func NewPlanController(isActive func() bool) *PlanController {
	return &PlanController{isActive: isActive}
}

// IsActive reports whether the session is currently in plan mode.
func (c *PlanController) IsActive() bool { return c.isActive() }

// Approve calls OnApprove, if set, with mode; OnApprove moves the gate
// out of plan mode.
func (c *PlanController) Approve(mode string) {
	if c.OnApprove != nil {
		c.OnApprove(mode)
	}
}

// PlanModeSparseReminder goes with each later prompt while plan mode is
// still on, once the model has had PlanModePrompt in full.
const PlanModeSparseReminder = "Plan mode is still on: research and read only, then present the plan with exit_plan_mode. Changes are refused until the plan is approved."

// PlanModeExitReminder tells the model the user left plan mode without
// approving a plan (Shift+Tab, /permissions), and which mode is now on.
func PlanModeExitReminder(mode string) string {
	return "The user has left plan mode; the permission mode is now \"" + mode + "\". You may make changes again. Do not call exit_plan_mode."
}
