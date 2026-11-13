//go:build !unix

package mods

import "os"

// pidAlive reports whether pid is still a running process. Off unix,
// os.FindProcess fails once the process is gone.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
