//go:build unix

package execenv

import (
	"os/exec"
	"syscall"
)

// SetProcGroup configures cmd to start its own process group (Setpgid), so
// the whole tree a shell spawns (including backgrounded jobs) can be
// killed by targeting the group rather than just the direct child.
func SetProcGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// KillProcessGroup sends sig to the whole process group led by pid (a
// process started with SetProcGroup, so pid is the group leader's pid). A
// negative pid targets the group in the kill(2) sense.
func KillProcessGroup(pid int, sig Signal) error {
	return syscall.Kill(-pid, sigNum(sig))
}

func sigNum(s Signal) syscall.Signal {
	if s == SignalTerm {
		return syscall.SIGTERM
	}
	return syscall.SIGKILL
}

// ProcGroupAlive reports whether anything is still running in the process
// group led by pid, by sending the null signal (0): POSIX kill(2) performs
// error checking but sends nothing, so this is a liveness probe.
func ProcGroupAlive(pid int) bool {
	return syscall.Kill(-pid, 0) == nil
}
