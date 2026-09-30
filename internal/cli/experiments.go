package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// Experiment switches for the benchmark rig (a planted-defect audit
// comparing kiln against Claude Code). Each is OFF by default and toggled
// by an environment variable, so a headless `kiln -p` run can A/B them
// without touching flags; with every variable unset, behaviour is
// byte-identical to a build with none of this code — every switch below
// gates its own effect behind its own os.Getenv check, same as
// headless_answers.go's HARNESS_PLAN_APPROVE/HARNESS_ASK_ANSWER. Kept
// small, isolated and easy to delete: each switch's wiring lives at the
// one call site named in its paragraph below, not spread through the
// package.
//
//	HARNESS_EXP_LEDGER=1              During plan mode, keep a findings
//	                                  ledger (ledgerPath, below) that the
//	                                  model appends a line to for every
//	                                  defect the moment it finds one, and
//	                                  builds the exit_plan_mode plan from.
//	                                  Wired: gate construction (chat.go,
//	                                  permission.GateOptions.PlanLedgerPath)
//	                                  narrowly exempts that one path from
//	                                  plan mode's read-only enforcement
//	                                  (permission.Gate.CheckWithOutcome);
//	                                  buildSystemPrompt appends the extra
//	                                  paragraph telling the model the path.
//	HARNESS_EXP_AUDIT=1               The first time the model ends its
//	                                  turn with no tool calls after a plan
//	                                  was approved, inject one message
//	                                  asking it to re-check the approved
//	                                  plan against the code, confirm each
//	                                  item has a test that ran, and fix
//	                                  anything missed — once per approval.
//	                                  Wired: an OnAfterResponse hook,
//	                                  chat.go, next to the other harness
//	                                  hook registrations.
//	HARNESS_EXP_REVIEW=1              Before finishing, dispatch one
//	                                  subagent (the task tool) to review
//	                                  the full `git diff` adversarially
//	                                  against the plan and report missed
//	                                  defects, then address its findings.
//	                                  Wired: an extra base-prompt
//	                                  paragraph, buildSystemPrompt.
//	HARNESS_EXP_PLAN_EFFORT=<level>   Use this reasoning effort
//	                                  (output_config.effort/ThinkingLevel)
//	                                  while the session is in plan mode;
//	                                  revert to the configured --effort
//	                                  once a plan is approved. Wired:
//	                                  agent.Start's ThinkingLevel and
//	                                  planController.OnApprove, chat.go.
//
// HARNESS_CACHE_RETENTION is documented separately, next to its one call
// site (internal/provider/api/anthropic_messages.go's cacheRetention):
// it lives in the provider layer, not here, because nothing in this
// package reads it.
const (
	envExpLedger     = "HARNESS_EXP_LEDGER"
	envExpAudit      = "HARNESS_EXP_AUDIT"
	envExpReview     = "HARNESS_EXP_REVIEW"
	envExpPlanEffort = "HARNESS_EXP_PLAN_EFFORT"
)

func ledgerEnabled() bool { return os.Getenv(envExpLedger) == "1" }
func auditEnabled() bool  { return os.Getenv(envExpAudit) == "1" }
func reviewEnabled() bool { return os.Getenv(envExpReview) == "1" }

// planEffortOverride is HARNESS_EXP_PLAN_EFFORT, trimmed; "" means unset
// (switch off).
func planEffortOverride() string {
	return strings.TrimSpace(os.Getenv(envExpPlanEffort))
}

// ledgerPath is HARNESS_EXP_LEDGER's findings ledger: one fixed file
// outside the project tree, mirroring Claude Code's own ~/.claude/plans/
// (docs/en/permission-modes's plan-mode write log lives outside the repo
// too, so a plan survives even though nothing in plan mode may touch the
// project). Returns "" if $HOME cannot be resolved, in which case the
// ledger experiment is silently unavailable rather than crashing the run.
func ledgerPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".harness", "plans", "ledger.md")
}

// ledgerPrompt is the paragraph buildSystemPrompt appends to the plan-mode
// prompt when HARNESS_EXP_LEDGER=1, telling the model where the ledger is
// and how to use it.
func ledgerPrompt(path string) string {
	return strings.Join([]string{
		"Findings ledger: as you research, the moment you notice something wrong",
		"(a bug, a missed edge case, code that contradicts its own docs or the",
		"spec), append one line to " + path + " with the file:line and what is",
		"wrong, before moving on to the next thing you read. This is the one file",
		"you may write or edit while in plan mode. Build the plan you present to",
		"exit_plan_mode from the ledger's contents, not from memory - re-read it",
		"first so nothing you already found gets dropped.",
	}, "\n")
}

// reviewPrompt is the paragraph buildSystemPrompt appends to the base
// prompt when HARNESS_EXP_REVIEW=1.
var reviewPrompt = strings.Join([]string{
	"Before you say you are done: dispatch one subagent with the task tool to",
	"review the full diff (`git diff`) adversarially against the plan you were",
	"given - have it look for missed defects, not just check the diff applies -",
	"and read its report. Address anything it finds before finishing.",
}, "\n")

// auditFollowUp is the message HARNESS_EXP_AUDIT=1 injects, once, the first
// time the model ends its turn with no tool calls after a plan was
// approved.
var auditFollowUp = strings.Join([]string{
	"Before finishing: re-check each item of the plan you presented against the",
	"code as it now stands. Confirm each one has a test that actually ran, and",
	"fix anything you missed. Then finish.",
}, "\n")
