//go:build windows

package execenv

import (
	"os/exec"
	"strconv"
)

// SetProcGroup is a no-op on Windows: there is no Setpgid equivalent, and
// none is needed, since KillProcessGroup below kills by process tree
// (parent/child ancestry) rather than by process group.
func SetProcGroup(cmd *exec.Cmd) {}

// KillProcessGroup kills pid's whole process tree with `taskkill /T /F`.
//
// Weaker than Unix in two ways:
//   - Windows has no SIGTERM a non-console process reliably reacts to
//     (GenerateConsoleCtrlEvent needs a shared console and a cooperating
//     child), so both Signal values force the tree down immediately; a
//     child never gets the graceful-shutdown chance SIGTERM gives it on
//     Unix.
//   - `taskkill /T` walks the tree by parent-PID ancestry recorded by
//     Windows, not by a process-group id, so a child that has been
//     reparented (e.g. its original parent already exited) can be missed;
//     Unix's process-group kill has no such gap.
func KillProcessGroup(pid int, _ Signal) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// ProcGroupAlive always reports false on Windows: there is no
// process-group liveness probe equivalent to Unix's kill(pid, 0), so a
// backgrounded job a command left running cannot be detected here. Weaker
// than Unix: such a job is never tracked, so KillLeftoverJobs cannot stop
// it on exit — it is left running instead of being cleaned up.
func ProcGroupAlive(pid int) bool {
	return false
}
