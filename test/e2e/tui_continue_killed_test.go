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
// opens the TUI against the same session. It must resume the interrupted
// operation (agent.ResumeIncomplete -> Lane.Resume re-runs the tool, gets
// the model's follow-up) and reach the idle prompt, not hang.
func TestTUI_Continue_AfterKilledPrintRun(t *testing.T) {
	addr, _ := startFaux(t, loadFauxScript(t, "kill-midtool"))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)

	// Seed: run `kiln -p` with bypassPermissions so the bash tool actually
	// executes (sleep 3), then SIGKILL it once the tool is in flight, leaving
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

	// Wait until the bash tool's args are persisted (it is executing the
	// sleep), then kill. Fail if we never see it in flight.
	if !waitForToolInFlight(t, sessDir, proj, 10*time.Second) {
		_ = cmd.Process.Kill()
		t.Fatal("seed run never reached an in-flight tool call")
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

	// Continue in the TUI. Before the fix this hung on a blank screen:
	// resume ran synchronously before program.Run() and its first tool
	// event blocked on tea.Program.Send forever. Now resume runs as a tea.Cmd
	// once the loop is live, so the app paints and drives the resumed op
	// through the real UI — including, in the default (manual) mode, the
	// permission prompt for the re-executed bash command.
	s := startTUI(t, 120, 40, proj, home, sessDir, addr, "--continue")
	waitReady(t, s)

	// The resumed op re-runs the interrupted `sleep` tool, which asks for
	// permission through the live UI (the deadlock never let this appear).
	if err := s.WaitFor("Allow kiln to run this command", 5*time.Second); err != nil {
		t.Fatalf("resumed op never reached the permission prompt: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	s.Send("1") // approve

	// After approval it completes the interrupted op and shows the follow-up.
	waitTurnSettled(t, s)
	joined := strings.Join(s.Rows(), "\n")
	if !strings.Contains(joined, "finished after resume") {
		t.Errorf("resumed session did not complete the interrupted op (no follow-up reply):\n%s", joined)
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
