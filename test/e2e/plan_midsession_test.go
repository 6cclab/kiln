//go:build e2e

// Package e2e: plan mode entered after startup. Plan mode is a permission
// mode like any other; the session must treat it the same whether it began
// in plan or reached it later. Each test's doc comment records the break
// used to confirm it can fail.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPlanMode_EnteredBySlashPlanReachesApprover: a session that starts in
// manual mode and enters plan with /plan gets the plan-mode instructions
// and, when the model presents its plan, the approver, not "Not in plan
// mode; no approval needed. Proceed directly." (the reply Andre's session
// got on 2026-10-15 while the footer still said plan).
// Break: plan state set only at startup (chat.go's old
// planController.SetActive(permissionMode == ModePlan)) -> fails.
func TestPlanMode_EnteredBySlashPlanReachesApprover(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	addr, srv := startFaux(t, headlessPlanScript)
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_MODEL"] = "faux/faux-1"
	env["HARNESS_PLAN_APPROVE"] = "acceptEdits"
	res := runHarness(t, proj, env, "-p", "/plan make done.txt", "--permission-mode", "manual")
	if res.Code != 0 {
		t.Fatalf("exit %d: %s", res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	if first := string(reqs[0].Messages); !strings.Contains(first, "You are in PLAN MODE.") {
		t.Errorf("first request does not tell the model it is in plan mode:\n%s", first)
	}
	var approved, proceedDirectly bool
	for _, r := range reqs {
		m := string(r.Messages)
		approved = approved || strings.Contains(m, "Plan approved")
		proceedDirectly = proceedDirectly || strings.Contains(m, "Not in plan mode")
	}
	if proceedDirectly || !approved {
		t.Fatalf("exit_plan_mode did not reach the approver (approved=%v, not-in-plan=%v)", approved, proceedDirectly)
	}
	if _, err := os.Stat(filepath.Join(proj, "done.txt")); err != nil {
		t.Errorf("approved plan's write did not land: %v", err)
	}
}

// TestPrompt_SystemPromptStableAcrossPlanExit: the system prompt does not
// change when a plan is approved, so leaving plan mode costs no prompt
// cache, and it never carries plan-mode text that outlives the mode.
// Break: put agent.PlanModePrompt back into buildSystemPrompt -> fails.
func TestPrompt_SystemPromptStableAcrossPlanExit(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	addr, srv := startFaux(t, headlessPlanScript)
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_MODEL"] = "faux/faux-1"
	env["HARNESS_PLAN_APPROVE"] = "acceptEdits"
	res := runHarness(t, proj, env, "-p", "make done.txt", "--permission-mode", "plan")
	if res.Code != 0 {
		t.Fatalf("exit %d: %s", res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("want requests before and after approval, got %d", len(reqs))
	}
	for i, r := range reqs {
		if r.System != reqs[0].System {
			t.Errorf("request %d's system prompt differs from the first", i)
		}
		if strings.Contains(r.System, "PLAN MODE") {
			t.Errorf("request %d's system prompt carries plan-mode text", i)
		}
	}
}

// TestTUI_PlanMode_EnteredMidSession_DialogAndFooter is Andre's report end
// to end in the real binary: start in manual, /plan, ask for a change. The
// plan must come up for approval ("Ready to code?"), and approving it must
// move the footer out of plan mode, instead of the model being told to
// proceed while the footer still said "plan only".
// Break: plan state set only at startup -> the dialog never appears.
func TestTUI_PlanMode_EnteredMidSession_DialogAndFooter(t *testing.T) {
	proj, home, sessDir, addr, requests := tuiFixture(t, headlessPlanScript)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "manual")
	waitReady(t, s)
	if err := s.WaitFor("ask before edits", 2*time.Second); err != nil {
		t.Fatalf("did not start in manual mode: %v", err)
	}
	s.Send("/plan")
	s.SendKey("enter")
	if err := s.WaitFor("plan only", 3*time.Second); err != nil {
		t.Fatalf("/plan did not move the footer to plan mode: %v", err)
	}
	s.Send("make done.txt")
	s.SendKey("enter")
	if err := s.WaitFor("Ready to code?", 5*time.Second); err != nil {
		t.Fatalf("the plan never came up for approval: %v", err)
	}
	s.SendKey("2") // Yes, manually approve edits
	if err := s.WaitFor("ask before edits", 5*time.Second); err != nil {
		t.Fatalf("footer did not leave plan mode after approval: %v", err)
	}
	// Manual mode asks before the plan's write, as it should.
	if err := s.WaitFor("Allow kiln to write to done.txt?", 5*time.Second); err != nil {
		t.Fatalf("manual mode did not ask before the approved plan's write: %v", err)
	}
	s.SendKey("1")
	waitTurnSettled(t, s)
	for _, r := range requests() {
		if strings.Contains(string(r), "Not in plan mode") {
			t.Fatal("exit_plan_mode told the model it was not in plan mode")
		}
	}
	if !strings.Contains(string(requests()[0]), "You are in PLAN MODE.") {
		t.Error("first request after /plan does not carry the plan-mode reminder")
	}
}
