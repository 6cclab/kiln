//go:build unix

package mods

import "syscall"

// pidAlive reports whether pid is still a running process.
func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
