//go:build !windows

package execenv

import "syscall"

func mkfifoForTest(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
