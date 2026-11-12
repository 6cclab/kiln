//go:build unix

package execenv

import "time"

func killScopedGroup(pid int, _ time.Time) error {
	return KillProcessGroup(pid, SignalKill)
}
