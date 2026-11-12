//go:build unix

package settings

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// ownProcessGroup starts cmd in its own process group, so a test can tell
// when everything it spawned (including detached process substitutions) is
// gone.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// waitProcessGroupGone blocks until no process is left in cmd's process
// group: a process substitution is not waited for by the shell that
// started it, and its stub logs only when it runs.
func waitProcessGroupGone(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd.Process == nil {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-cmd.Process.Pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("processes of pgid %d still running", cmd.Process.Pid)
}
