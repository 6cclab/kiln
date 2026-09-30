package cli

import (
	"context"
	"os"

	"github.com/andrepato/harness/internal/tools"
)

// Headless answers let an unattended run (-p, the benchmark rig) take the
// same path a person clicking through the TUI would, so plan mode and
// ask_user_question can be measured without a terminal. They only fill the
// default approvers: the TUI rebinds both, so an interactive session never
// sees them.
//
//	HARNESS_PLAN_APPROVE=<mode>  approve every plan, leaving plan mode into
//	                             <mode> (auto, acceptEdits, ...), as picking
//	                             "Yes, and use auto mode" does.
//	HARNESS_ASK_ANSWER=first     answer each question with its first option.
const (
	envPlanApprove = "HARNESS_PLAN_APPROVE"
	envAskAnswer   = "HARNESS_ASK_ANSWER"
)

// headlessPlanApprover returns an approver that approves into the mode in
// HARNESS_PLAN_APPROVE, or nil when the variable is unset.
func headlessPlanApprover() tools.PlanApprover {
	mode := os.Getenv(envPlanApprove)
	if mode == "" {
		return nil
	}
	return func(context.Context, string) (tools.PlanDecision, error) {
		return tools.PlanDecision{Kind: tools.PlanDecisionApprove, Mode: mode}, nil
	}
}

// headlessAskApprover returns an approver that picks each question's first
// option when HARNESS_ASK_ANSWER=first, or nil otherwise.
func headlessAskApprover() tools.AskUserApprover {
	if os.Getenv(envAskAnswer) != "first" {
		return nil
	}
	return func(_ context.Context, questions []tools.AskUserQuestion) ([]tools.AskUserAnswer, error) {
		answers := make([]tools.AskUserAnswer, len(questions))
		for i, q := range questions {
			answers[i] = tools.AskUserAnswer{Question: q.Question, Answers: []string{q.Options[0].Label}}
		}
		return answers, nil
	}
}
