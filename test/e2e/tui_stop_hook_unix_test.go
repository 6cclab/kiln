//go:build e2e && unix

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTUI_Esc_KillsASlowStopHook: Stop hooks run while the turn is still
// live, on its context, so Esc during a slow Stop hook kills it (its
// process group) at once, as Esc kills a PreToolUse hook. They used to run
// after the run had ended, where Esc no longer reached them, so a slow
// Stop hook ran to its timeout whatever the user did.
func TestTUI_Esc_KillsASlowStopHook(t *testing.T) {
	script := "model: faux-1\nsteps:\n  - text: \"All done here.\"\n    end_turn: true\n"
	proj, home, sessDir, addr, _ := tuiFixture(t, script)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "stop.pid")
	marker := filepath.Join(dir, "stop-finished")
	writeHookSettings(t, proj, "Stop", "", fmt.Sprintf("echo $$ > %s; sleep 30; touch %s", pidFile, marker))

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("finish up")
	s.SendKey("enter")
	if err := s.WaitFor("All done here.", 5*time.Second); err != nil {
		t.Fatalf("the reply never arrived: %v", err)
	}
	pid := waitPidFile(t, pidFile, 5*time.Second)

	s.SendKey("esc")
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the Stop hook (pid %d) is still running 5s after Esc", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the Stop hook ran to completion despite Esc")
	}
	if strings.Contains(strings.Join(s.Rows(), "\n"), "Stop hook asked to continue") {
		t.Fatal("an interrupted Stop hook's verdict was reported")
	}
	// As in Claude Code, Esc during the Stop hook interrupts the turn.
	if err := s.WaitFor("Interrupted. Tell kiln what to do instead.", 5*time.Second); err != nil {
		t.Fatalf("the turn did not end as interrupted: %v", err)
	}
}

// TestTUI_StopHook_SkippedWhenTheTurnIsInterrupted: as in Claude Code, a
// Stop hook runs only when a turn completes, not when the user interrupts
// it. It used to run at every run end, including an interrupted one.
func TestTUI_StopHook_SkippedWhenTheTurnIsInterrupted(t *testing.T) {
	script := "model: faux-1\nsteps:\n  - chunk_delay: 60ms\n    text: \"" +
		strings.Repeat("Slow words keep streaming in. ", 30) + "\"\n    end_turn: true\n"
	proj, home, sessDir, addr, _ := tuiFixture(t, script)
	ran := filepath.Join(t.TempDir(), "stop-ran")
	writeHookSettings(t, proj, "Stop", "", "touch "+ran)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("write slowly")
	s.SendKey("enter")
	if err := s.WaitFor("Slow words keep streaming", 5*time.Second); err != nil {
		t.Fatalf("the reply never started streaming: %v", err)
	}
	s.SendKey("esc")
	if err := s.WaitFor("Interrupted. Tell kiln what to do instead.", 5*time.Second); err != nil {
		t.Fatalf("never saw the interrupted note: %v", err)
	}
	time.Sleep(time.Second)
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the Stop hook ran for a turn the user interrupted")
	}
}

func waitPidFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Stop hook never started (no pid in %s)", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestTUI_StopHook_BlockContinuesTheTurn: in the TUI too, a Stop hook that
// exits 2 keeps the turn going, as in Claude Code: the reason is shown,
// the model replies again, and the turn then ends normally.
func TestTUI_StopHook_BlockContinuesTheTurn(t *testing.T) {
	script := "model: faux-1\nsteps:\n  - text: \"First reply here.\"\n    end_turn: true\n  - text: \"Second reply after the feedback.\"\n    end_turn: true\n"
	proj, home, sessDir, addr, requests := tuiFixture(t, script)
	writeHookSettings(t, proj, "Stop", "", hookScript(t, "stop-block-once.sh"))

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("finish up")
	s.SendKey("enter")
	if err := s.WaitFor("Second reply after the feedback.", 10*time.Second); err != nil {
		t.Fatalf("the model never replied after the Stop hook's feedback: %v", err)
	}
	if err := s.WaitFor("Stop hook asked to continue: run the tests first", 5*time.Second); err != nil {
		t.Fatalf("the hook's reason was not shown: %v", err)
	}
	waitTurnSettled(t, s)
	if n := len(requests()); n != 2 {
		t.Fatalf("%d model requests, want 2", n)
	}
	if screen := strings.Join(s.Rows(), "\n"); strings.Contains(screen, "Interrupted") {
		t.Fatalf("the continued turn ended as interrupted:\n%s", screen)
	}
}

// TestTUI_Esc_InterruptsAContinuedTurn: the lane stays busy through a Stop
// hook's continuation, so Esc interrupts the continued reply like any
// other, and nothing continues it again.
func TestTUI_Esc_InterruptsAContinuedTurn(t *testing.T) {
	script := "model: faux-1\nsteps:\n  - text: \"First reply here.\"\n    end_turn: true\n  - chunk_delay: 60ms\n    text: \"" +
		strings.Repeat("Slow words keep streaming in. ", 30) + "\"\n    end_turn: true\n"
	proj, home, sessDir, addr, requests := tuiFixture(t, script)
	writeHookSettings(t, proj, "Stop", "", hookScript(t, "block-exit2.sh"))

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("finish up")
	s.SendKey("enter")
	if err := s.WaitFor("Slow words keep streaming", 10*time.Second); err != nil {
		t.Fatalf("the continued reply never started: %v", err)
	}
	s.SendKey("esc")
	if err := s.WaitFor("Interrupted. Tell kiln what to do instead.", 5*time.Second); err != nil {
		t.Fatalf("never saw the interrupted note: %v", err)
	}
	time.Sleep(time.Second)
	if n := len(requests()); n != 2 {
		t.Fatalf("%d model requests, want 2: an interrupted turn must not be continued", n)
	}
}
