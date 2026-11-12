//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/session/jsonl"
)

// TestTUI_Continue_AfterKilledPrintRun is the regression for finding
// continue-hangs-after-killed-print-run: a `kiln -p` run is SIGKILLed while
// a bash tool call is in flight (so the session is left with an incomplete
// operation — a tool_use persisted with no outcome), then `kiln --continue`
// opens the TUI against the same session. It must show the uncertain
// outcome without replaying the tool, reach an idle prompt, and accept a
// new user turn rather than hanging.
func TestTUI_Continue_AfterKilledPrintRun(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	script := strings.Replace(loadFauxScript(t, "kill-midtool"), "sleep 1 && echo done", "printf x >> resume-effect.txt; sleep 5", 1)
	addr, srv := startFaux(t, script)
	env := baseEnv(home, sessDir, addr)
	marker := filepath.Join(proj, "resume-effect.txt")

	// Seed: run `kiln -p` with bypassPermissions so the bash tool actually
	// executes, then SIGKILL it once the tool is in flight, leaving
	// the operation incomplete.
	cmd := exec.Command(harnessBin, "-p", "run the sleep command",
		"--permission-mode", "bypassPermissions", "--output-format", "text")
	cmd.Dir = proj
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start seed: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Wait until the bash tool's args are persisted and its first effect has
	// happened, then kill while it is sleeping.
	if !waitForToolInFlight(t, sessDir, proj, 10*time.Second) {
		_ = cmd.Process.Kill()
		t.Fatal("seed run never reached an in-flight tool call")
	}
	markerDeadline := time.Now().Add(5 * time.Second)
	for {
		body, err := os.ReadFile(marker)
		if err == nil && string(body) == "x" {
			break
		}
		if time.Now().After(markerDeadline) {
			_ = cmd.Process.Kill()
			t.Fatal("seed command never applied its first marker effect")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("seed run did not exit after SIGKILL")
	}

	// The session must now hold an incomplete operation (a tool_args write
	// with no matching outcome), otherwise the repro's precondition is gone.
	if !sessionHasIncompleteOp(t, sessDir, proj) {
		t.Fatal("precondition failed: killed seed left no incomplete operation")
	}

	// Continue in the TUI. The old implementation waited for a permission
	// prompt to re-execute the command; recovery now reports uncertainty
	// and returns to the editor without replaying an already-applied effect.
	s := startTUI(t, 120, 40, proj, home, sessDir, addr, "--continue")
	waitReady(t, s)
	if err := s.WaitFor("effect is unknown", 5*time.Second); err != nil {
		t.Fatalf("resumed op never showed the uncertain effect: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	waitTurnSettled(t, s)
	if body, err := os.ReadFile(marker); err != nil || string(body) != "x" {
		t.Fatalf("marker after resume = %q, err=%v; command was replayed", body, err)
	}
	if got := len(srv.Requests()); got != 1 {
		t.Fatalf("model requests after uncertain recovery = %d, want one original request", got)
	}
	joined := strings.Join(s.Rows(), "\n")
	if strings.Contains(joined, "Allow kiln to run this command") {
		t.Fatalf("recovery asked to replay uncertain command:\n%s", joined)
	}
	// The paired error result is now on the branch; the user can issue a
	// fresh turn without leaving a dangling tool call in the transcript.
	s.Send("what happened?")
	s.SendKey("enter")
	if err := s.WaitFor("finished after resume", 5*time.Second); err != nil {
		t.Fatalf("new prompt did not receive a model reply: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
}

// waitForToolInFlight polls the session file until a pi.op.tool_args entry
// appears (the bash tool's arguments are persisted as it begins executing).
func waitForToolInFlight(t *testing.T, sessDir, proj string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sessionContains(sessDir, proj, `"namespace":"pi.op.tool_args"`) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// sessionContains reports whether any session file for proj contains needle.
func sessionContains(sessDir, proj, needle string) bool {
	dir := filepath.Join(sessDir, jsonl.DirectoryName(proj))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil && strings.Contains(string(data), needle) {
			return true
		}
	}
	return false
}

// sessionHasIncompleteOp reports whether the session's latest op.state is a
// running/pending state (not a finished one) — the incomplete-operation
// precondition the repro depends on.
func sessionHasIncompleteOp(t *testing.T, sessDir, proj string) bool {
	t.Helper()
	// tool_args written but the op never reached a terminal state: the
	// cheapest observable proxy is that an op.tool_args entry exists while no
	// tool_result / outcome entry does. We look for the args write and the
	// absence of an "outcome" op.state.
	return sessionContains(sessDir, proj, `"namespace":"pi.op.tool_args"`) &&
		!sessionContains(sessDir, proj, `"at":"outcome_ready"`)
}
