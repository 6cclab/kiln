//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// bashBackgroundScript runs a command that backgrounds a long job, the way
// a model starts a server before curling it.
const bashBackgroundScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "sleep 120 & echo $! > job.pid; echo started"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`

// TestPrint_BashBackgroundJob_DoesNotHangAndDiesWithSession: the backgrounded
// job used to hold the tool's output pipe, so the call never returned (a
// real session sat 29 minutes on "go run . &"). Now the call returns once
// the shell exits, and the job is stopped when the session ends
// (qa/findings *bash-hangs-on-background-job).
func TestPrint_BashBackgroundJob_DoesNotHangAndDiesWithSession(t *testing.T) {
	addr, _ := startFaux(t, bashBackgroundScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	start := time.Now()
	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "start a job",
		"--permission-mode", "bypassPermissions",
	)
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("run took %s; the bash call waited on the background job", elapsed)
	}
	if res.Code != 0 || !strings.Contains(res.Stdout, "done") {
		t.Fatalf("exit %d, stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}

	raw, err := os.ReadFile(filepath.Join(proj, "job.pid"))
	if err != nil {
		t.Fatalf("job.pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("job.pid = %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	waitGone(t, pid)
}

// TestTUI_Hangup_StopsBackgroundJobs: closing the terminal window sends
// SIGHUP, which used to end kiln on the spot and skip its exit path, so a
// server the session started kept its port. Now kiln stops, runs its
// cleanup, and exits 129.
func TestTUI_Hangup_StopsBackgroundJobs(t *testing.T) {
	addr, _ := startFaux(t, bashBackgroundScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	defer s.Close()
	if err := s.WaitFor("describe a task", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	s.Send("start a job\r")
	if err := s.WaitFor(regexp.MustCompile(`(?m)^\s*done\s*$`), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(proj, "job.pid"))
	if err != nil {
		t.Fatalf("job.pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("job.pid = %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	if err := syscall.Kill(s.Pid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	// A non-zero exit also comes back as err; the code is what matters.
	if code, err := s.Exit(); code != 129 {
		t.Errorf("exit code = %d (%v), want 129 (stopped by SIGHUP through the exit path)", code, err)
	}
	waitGone(t, pid)
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("background job %d still running after the session ended", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
