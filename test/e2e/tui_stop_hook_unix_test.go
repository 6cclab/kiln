//go:build e2e && unix

package e2e

import (
	"encoding/json"
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

// TestTUI_Esc_KillsBothSlowStopHooks: with two Stop hooks configured, Esc
// kills both, not just whichever a sequential chain happened to be on -
// they run together (RunHooksConcurrently), so cancelling the shared ctx
// must reach every one of them.
func TestTUI_Esc_KillsBothSlowStopHooks(t *testing.T) {
	script := "model: faux-1\nsteps:\n  - text: \"All done here.\"\n    end_turn: true\n"
	proj, home, sessDir, addr, _ := tuiFixture(t, script)
	dir := t.TempDir()
	pidFileA := filepath.Join(dir, "stop-a.pid")
	markerA := filepath.Join(dir, "stop-a-finished")
	pidFileB := filepath.Join(dir, "stop-b.pid")
	markerB := filepath.Join(dir, "stop-b-finished")
	gapWriteHooks(t, proj, "Stop", "",
		fmt.Sprintf("echo $$ > %s; sleep 30; touch %s", pidFileA, markerA),
		fmt.Sprintf("echo $$ > %s; sleep 30; touch %s", pidFileB, markerB),
	)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("finish up")
	s.SendKey("enter")
	if err := s.WaitFor("All done here.", 5*time.Second); err != nil {
		t.Fatalf("the reply never arrived: %v", err)
	}
	pidA := waitPidFile(t, pidFileA, 5*time.Second)
	pidB := waitPidFile(t, pidFileB, 5*time.Second)

	s.SendKey("esc")
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pidA, 0) == nil || syscall.Kill(pidB, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("a Stop hook is still running 5s after Esc (pids %d, %d)", pidA, pidB)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(markerA); err == nil {
		t.Error("the first Stop hook ran to completion despite Esc")
	}
	if _, err := os.Stat(markerB); err == nil {
		t.Error("the second Stop hook ran to completion despite Esc")
	}
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
	command := hookScript(t, "stop-block-once.sh")
	writeHookSettings(t, proj, "Stop", "", command)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("finish up")
	s.SendKey("enter")
	if err := s.WaitFor("Second reply after the feedback.", 10*time.Second); err != nil {
		t.Fatalf("the model never replied after the Stop hook's feedback: %v", err)
	}
	// The exit-2 reason is prefixed with the command, as Claude Code's own
	// hook runner does - kiln used to show the hook's stderr alone. Checked
	// as separate short substrings, not the whole bracketed command in one
	// WaitFor: the fixture's absolute path is long enough in some checkouts
	// to wrap across a transcript row at an arbitrary point, which would
	// break a single contiguous match that happens to straddle the wrap.
	if err := s.WaitFor("Stop hook asked to continue: [", 5*time.Second); err != nil {
		t.Fatalf("the bracketed command form was not shown: %v", err)
	}
	screen := strings.Join(s.Rows(), "\n")
	if !strings.Contains(screen, "stop-block-once.sh") {
		t.Errorf("screen = %q, want the hook's command named in the reason", screen)
	}
	if !strings.Contains(screen, "run the tests first") {
		t.Errorf("screen = %q, want the hook's reason shown to the user", screen)
	}
	waitTurnSettled(t, s)
	if n := len(requests()); n != 2 {
		t.Fatalf("%d model requests, want 2", n)
	}
	if screen := strings.Join(s.Rows(), "\n"); strings.Contains(screen, "Interrupted") {
		t.Fatalf("the continued turn ended as interrupted:\n%s", screen)
	}
}

// TestTUI_StopHook_AdditionalContextNeverShown: as in the -p path
// (TestHooks_StopBlock_AdditionalContextNeverReachesTheModel), a Stop
// hook's hookSpecificOutput.additionalContext alongside a block never
// reaches the model or the screen in the TUI either - only the block's
// own reason does.
func TestTUI_StopHook_AdditionalContextNeverShown(t *testing.T) {
	script := "model: faux-1\nsteps:\n  - text: \"First reply here.\"\n    end_turn: true\n  - text: \"Second reply after the feedback.\"\n    end_turn: true\n"
	proj, home, sessDir, addr, requests := tuiFixture(t, script)
	hook := filepath.Join(t.TempDir(), "block-with-context.sh")
	body := "#!/bin/bash\ninput=$(cat)\ncase \"$input\" in *'\"stop_hook_active\":true'*) exit 0;; esac\n" +
		`echo '{"decision":"block","reason":"coverage dropped","hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"internal build id 7f3a9c"}}'` + "\n"
	if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHookSettings(t, proj, "Stop", "", hook)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("finish up")
	s.SendKey("enter")
	if err := s.WaitFor("Second reply after the feedback.", 10*time.Second); err != nil {
		t.Fatalf("the model never replied after the Stop hook's feedback: %v", err)
	}
	if err := s.WaitFor("coverage dropped", 5*time.Second); err != nil {
		t.Fatalf("the block's own reason was not shown: %v", err)
	}
	waitTurnSettled(t, s)
	if screen := strings.Join(s.Rows(), "\n"); strings.Contains(screen, "internal build id 7f3a9c") {
		t.Errorf("screen = %q, must not show the hook's additionalContext", screen)
	}
	for _, r := range requests() {
		if strings.Contains(string(r), "internal build id 7f3a9c") {
			t.Errorf("a model request carried the hook's additionalContext: %s", r)
		}
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

// While a Stop hook runs, the busy row says so ("running stop hook"), as
// Claude Code's spinner does, rather than looking like the model is still
// working; a hook's own statusMessage replaces that text.
func TestTUI_StopHook_BusyRowNamesTheHook(t *testing.T) {
	for _, tc := range []struct {
		name, statusMessage, want string
	}{
		{"default", "", "running stop hook"},
		{"statusMessage", "Checking the test suite", "Checking the test suite…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "model: faux-1\nsteps:\n  - text: \"All done here.\"\n    end_turn: true\n"
			proj, home, sessDir, addr, _ := tuiFixture(t, script)
			hook := map[string]any{"type": "command", "command": "sleep 30"}
			if tc.statusMessage != "" {
				hook["statusMessage"] = tc.statusMessage
			}
			data, err := json.Marshal(map[string]any{"hooks": map[string]any{
				"Stop": []map[string]any{{"hooks": []map[string]any{hook}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}

			s := startTUI(t, 100, 30, proj, home, sessDir, addr)
			waitReady(t, s)
			s.Send("finish up")
			s.SendKey("enter")
			if err := s.WaitFor("All done here.", 5*time.Second); err != nil {
				t.Fatalf("the reply never arrived: %v", err)
			}
			if err := s.WaitFor(tc.want, 5*time.Second); err != nil {
				t.Fatalf("the busy row never named the Stop hook: %v", err)
			}
			row := ""
			for _, r := range s.Rows() {
				if strings.Contains(r, tc.want) {
					row = r
				}
			}
			if !strings.Contains(row, "esc to stop") {
				t.Errorf("%q is not on the busy row: %q", tc.want, row)
			}
			s.SendKey("esc")
			if err := s.WaitFor("Interrupted. Tell kiln what to do instead.", 5*time.Second); err != nil {
				t.Fatalf("Esc did not end the Stop hook: %v", err)
			}
			if strings.Contains(strings.Join(s.Rows(), "\n"), tc.want) {
				t.Errorf("%q is still on screen after the hook ended", tc.want)
			}
		})
	}
}
