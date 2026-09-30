//go:build e2e

// Package e2e: the HARNESS_EXP_* / HARNESS_CACHE_RETENTION experiment
// switches (internal/cli/experiments.go), each OFF by default. Every test
// here proves both directions: the switch changes behaviour when set, and
// changes nothing when unset. Each test's doc comment records the break
// used to confirm it can fail.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- switch 1: HARNESS_EXP_LEDGER -----------------------------------------

// TestExpLedger_PromptNamesThePathOnlyWhenSet: the plan-mode prompt gets an
// extra paragraph naming the ledger path when HARNESS_EXP_LEDGER=1, and
// stays exactly agent.PlanModePrompt (no ledger paragraph) when unset.
// Break: drop the ledgerPrompt append in buildSystemPrompt -> the "set"
// case's assertion on "Findings ledger" fails.
func TestExpLedger_PromptNamesThePathOnlyWhenSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		wantIn bool
	}{
		{"set", map[string]string{"HARNESS_EXP_LEDGER": "1"}, true},
		{"unset", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			sys := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "", tc.env, "--permission-mode", "plan")
			gotLedger := strings.Contains(sys, "Findings ledger")
			if gotLedger != tc.wantIn {
				t.Errorf("system prompt contains ledger paragraph = %v, want %v:\n%s", gotLedger, tc.wantIn, sys)
			}
			wantPath := filepath.Join(home, ".harness", "plans", "ledger-")
			gotPath := strings.Contains(sys, wantPath)
			if gotPath != tc.wantIn {
				t.Errorf("system prompt contains ledger path = %v, want %v", gotPath, tc.wantIn)
			}
		})
	}
}

// expLedgerWriteScript, with ledgerPath spliced in, writes the ledger first
// (w1), then a second file elsewhere (w2) that plan mode's read-only
// enforcement should still refuse.
const expLedgerWriteScriptFmt = `model: faux-1
steps:
  - tool_call: {name: write, args: {path: %q, content: "math.js:2 subtracts instead of adding"}, id: w1}
  - on_tool_result: w1
    then:
      - tool_call: {name: write, args: {path: "elsewhere.txt", content: "nope"}, id: w2}
      - on_tool_result: w2
        then:
          - text: "noted"
`

// TestExpLedger_WriteAllowedOnlyForLedgerPath: with HARNESS_EXP_LEDGER=1,
// plan mode's read-only enforcement lets a write land on exactly the
// ledger path and still refuses one anywhere else; with it unset, both are
// refused, as plan mode always refused writes before this switch existed.
// Break: in permission.go, drop the planLedgerPath exception check (or
// widen it to match any path) -> the "set" case's ledger-exists assertion
// fails (dropped) or the elsewhere.txt-refused assertion fails (widened).
func TestExpLedger_WriteAllowedOnlyForLedgerPath(t *testing.T) {
	for _, tc := range []struct {
		name          string
		env           map[string]string
		wantLedger    bool
		wantElsewhere bool
	}{
		{"set", map[string]string{"HARNESS_EXP_LEDGER": "LEDGER"}, true, false},
		{"unset", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			ledger := filepath.Join(home, ".harness", "plans", "ledger.md")
			script := fmt.Sprintf(expLedgerWriteScriptFmt, ledger)
			addr, _ := startFaux(t, script)
			env := baseEnv(home, sessDir, addr)
			env["HARNESS_MODEL"] = "faux/faux-1"
			for k, v := range tc.env {
				if v == "LEDGER" {
					v = ledger
				}
				env[k] = v
			}
			res := runHarness(t, proj, env, "-p", "audit math.js", "--permission-mode", "plan")
			_ = res

			_, ledgerErr := os.Stat(ledger)
			if gotLedger := ledgerErr == nil; gotLedger != tc.wantLedger {
				t.Errorf("ledger written=%v, want %v (exit %d, stderr %s)", gotLedger, tc.wantLedger, res.Code, res.Stderr)
			}
			_, elseErr := os.Stat(filepath.Join(proj, "elsewhere.txt"))
			if gotElse := elseErr == nil; gotElse != tc.wantElsewhere {
				t.Errorf("elsewhere.txt written=%v, want %v", gotElse, tc.wantElsewhere)
			}
		})
	}
}

// --- switch 2: HARNESS_EXP_AUDIT ------------------------------------------

const expAuditScript = `model: faux-1
steps:
  - tool_call: {name: exit_plan_mode, args: {plan: "Write done.txt."}, id: p1}
  - on_tool_result: p1
    then:
      - tool_call: {name: write, args: {path: done.txt, content: "ok"}, id: w1}
      - on_tool_result: w1
        then:
          - text: "Wrote it."
  - text: "Audited, all good."
`

// TestExpAudit_InjectsOneFollowUpAfterApproval: with HARNESS_EXP_AUDIT=1,
// after the plan is approved and the model ends its turn with no tool
// calls, kiln sends one more request carrying the audit follow-up text and
// the script's trailing turn ("Audited, all good.") answers it -- so the
// faux server ends up with one more recorded request than the unset case,
// and the last request's Messages carries the follow-up text. Unset: the
// run ends after "Wrote it." as before, one request fewer.
// Break: drop the OnAfterResponse hook (or its auditArmed.Store(true) in
// OnApprove) -> the "set" case's extra-request and follow-up-text
// assertions both fail.
func TestExpAudit_InjectsOneFollowUpAfterApproval(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       map[string]string
		wantExtra bool
	}{
		{"set", map[string]string{"HARNESS_EXP_AUDIT": "1"}, true},
		{"unset", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			addr, srv := startFaux(t, expAuditScript)
			env := baseEnv(home, sessDir, addr)
			env["HARNESS_MODEL"] = "faux/faux-1"
			env["HARNESS_PLAN_APPROVE"] = "auto"
			for k, v := range tc.env {
				env[k] = v
			}
			res := runHarness(t, proj, env, "-p", "make done.txt", "--permission-mode", "plan")
			if res.Code != 0 {
				t.Fatalf("exit %d: %s", res.Code, res.Stderr)
			}
			reqs := srv.Requests()
			last := string(reqs[len(reqs)-1].Messages)
			gotFollowUp := strings.Contains(last, "re-check each item of the plan")
			if gotFollowUp != tc.wantExtra {
				t.Errorf("last request carries the audit follow-up = %v, want %v:\n%s", gotFollowUp, tc.wantExtra, last)
			}
			wantReqs := 3 // initial prompt, exit_plan_mode's tool result, write's tool result
			if tc.wantExtra {
				wantReqs = 4 // plus the injected audit follow-up's own request
			}
			if len(reqs) != wantReqs {
				t.Errorf("recorded %d requests, want %d", len(reqs), wantReqs)
			}
		})
	}
}

// --- switch 3: HARNESS_EXP_REVIEW -----------------------------------------

// TestExpReview_BasePromptParagraphOnlyWhenSet: the base prompt gets the
// reviewer-subagent paragraph when HARNESS_EXP_REVIEW=1, and does not
// otherwise -- in or out of plan mode.
// Break: drop the reviewEnabled() append in buildSystemPrompt -> the "set"
// case's assertion fails.
func TestExpReview_BasePromptParagraphOnlyWhenSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		wantIn bool
	}{
		{"set", map[string]string{"HARNESS_EXP_REVIEW": "1"}, true},
		{"unset", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			sys := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "", tc.env)
			got := strings.Contains(sys, "dispatch one subagent")
			if got != tc.wantIn {
				t.Errorf("system prompt contains reviewer paragraph = %v, want %v:\n%s", got, tc.wantIn, sys)
			}
		})
	}
}

// --- switch 4: HARNESS_EXP_PLAN_EFFORT ------------------------------------

const expPlanEffortScript = `model: faux-1
steps:
  - tool_call: {name: exit_plan_mode, args: {plan: "Write done.txt."}, id: p1}
  - on_tool_result: p1
    then:
      - text: "Done."
`

type wireThinking struct {
	Thinking *struct {
		BudgetTokens int `json:"budget_tokens"`
	} `json:"thinking"`
}

func budgetTokensOf(t *testing.T, body json.RawMessage) int {
	t.Helper()
	var w wireThinking
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if w.Thinking == nil {
		return 0
	}
	return w.Thinking.BudgetTokens
}

// TestExpPlanEffort_RaisesEffortDuringPlanModeOnly: with
// HARNESS_EXP_PLAN_EFFORT=high and --effort medium, the first request
// (still in plan mode) carries high's thinking budget and the request
// after approval carries medium's; with the switch unset, both requests
// carry medium's budget (--effort applies throughout, as before).
// Break: drop the startEffort override (session always starts at
// baseEffort) -> the "set" case's first-request assertion fails. Drop the
// OnApprove revert -> its second-request assertion fails instead.
func TestExpPlanEffort_RaisesEffortDuringPlanModeOnly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		env        map[string]string
		wantFirst  int
		wantSecond int
	}{
		{"set", map[string]string{"HARNESS_EXP_PLAN_EFFORT": "high"}, 16383, 8192},
		{"unset", nil, 8192, 8192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			addr, srv := startFaux(t, expPlanEffortScript)
			env := baseEnv(home, sessDir, addr)
			env["HARNESS_MODEL"] = "faux/faux-1"
			env["HARNESS_PLAN_APPROVE"] = "auto"
			for k, v := range tc.env {
				env[k] = v
			}
			res := runHarness(t, proj, env, "-p", "make done.txt", "--permission-mode", "plan", "--effort", "medium")
			if res.Code != 0 {
				t.Fatalf("exit %d: %s", res.Code, res.Stderr)
			}
			reqs := srv.Requests()
			if len(reqs) < 2 {
				t.Fatalf("got %d requests, want at least 2", len(reqs))
			}
			if got := budgetTokensOf(t, reqs[0].Body); got != tc.wantFirst {
				t.Errorf("first request (plan mode) budget_tokens = %d, want %d", got, tc.wantFirst)
			}
			if got := budgetTokensOf(t, reqs[1].Body); got != tc.wantSecond {
				t.Errorf("second request (post-approval) budget_tokens = %d, want %d", got, tc.wantSecond)
			}
		})
	}
}
