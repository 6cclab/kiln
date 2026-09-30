//go:build e2e

// Package e2e: headless answers. HARNESS_PLAN_APPROVE and
// HARNESS_ASK_ANSWER let a -p run follow plan mode and ask_user_question
// the way a person answering in the TUI would. Each test's doc comment
// records the break used to confirm it can fail.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const headlessPlanScript = `model: faux-1
steps:
  - tool_call: {name: exit_plan_mode, args: {plan: "Write done.txt."}, id: p1}
  - on_tool_result: p1
    then:
      - tool_call: {name: write, args: {path: done.txt, content: "ok"}, id: w1}
      - on_tool_result: w1
        then:
          - text: "Wrote it."
`

// TestHeadless_PlanApproveEnvApprovesAndBuilds: with HARNESS_PLAN_APPROVE
// the plan is approved into auto mode and the write lands; without it,
// print mode keeps refusing plan-mode changes.
// Break: drop the headlessPlanApprover call in chat.go -> done.txt missing.
func TestHeadless_PlanApproveEnvApprovesAndBuilds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		written bool
	}{
		{"approved", map[string]string{"HARNESS_PLAN_APPROVE": "auto"}, true},
		{"unset", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			addr, srv := startFaux(t, headlessPlanScript)
			env := baseEnv(home, sessDir, addr)
			env["HARNESS_MODEL"] = "faux/faux-1"
			for k, v := range tc.env {
				env[k] = v
			}
			res := runHarness(t, proj, env, "-p", "make done.txt", "--permission-mode", "plan")
			_, err := os.Stat(filepath.Join(proj, "done.txt"))
			if written := err == nil; written != tc.written {
				t.Fatalf("done.txt written=%v, want %v (exit %d, stderr %s)", written, tc.written, res.Code, res.Stderr)
			}
			if tc.written {
				var sawApproval bool
				for _, r := range srv.Requests() {
					if strings.Contains(string(r.Messages), `Plan approved`) && strings.Contains(string(r.Messages), `\"auto\"`) {
						sawApproval = true
					}
				}
				if !sawApproval {
					t.Errorf("no request carried the approval into auto mode")
				}
			}
		})
	}
}

// TestHeadless_AskAnswerFirstPicksFirstOption: HARNESS_ASK_ANSWER=first
// answers with each question's first option instead of the no-user error.
// Break: drop the headlessAskApprover call -> the model gets "No user".
func TestHeadless_AskAnswerFirstPicksFirstOption(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	script := loadFauxScript(t, "askuser-behaviour")
	addr, srv := startFaux(t, script)
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_MODEL"] = "faux/faux-1"
	env["HARNESS_ASK_ANSWER"] = "first"
	res := runHarness(t, proj, env, "-p", "decide")
	if res.Code != 0 {
		t.Fatalf("exit %d: %s", res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	last := string(reqs[len(reqs)-1].Messages)
	if !strings.Contains(last, `Which approach should I use?`) || !strings.Contains(last, `Fast path`) || strings.Contains(last, "No user is available") {
		t.Fatalf("answer did not reach the model as the first option:\n%s", last)
	}
}
