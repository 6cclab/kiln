//go:build darwin || freebsd || netbsd || openbsd || linux

package screen

import "golang.org/x/sys/unix"

// RawMode reports whether the PTY's line discipline is in raw mode (no
// canonical input, no echo): the state a TUI sets and must undo on exit.
// The master and slave share one termios, so this reads it through the
// master, which stays open after the program exits.
func (s *Screen) RawMode() (bool, error) {
	t, err := unix.IoctlGetTermios(int(s.ptmx.Fd()), ioctlGetTermios)
	if err != nil {
		return false, err
	}
	return t.Lflag&(unix.ICANON|unix.ECHO) == 0, nil
}
