package screen

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Signal sends sig to the driven process (SIGTERM, SIGHUP, SIGQUIT for a
// goroutine dump), unlike Exit, which only ever sends SIGINT.
func (s *Screen) Signal(sig os.Signal) error {
	if s.cmd.Process == nil {
		return fmt.Errorf("screen.Signal: no process")
	}
	return s.cmd.Process.Signal(sig)
}

// WaitExit waits up to timeout for the process to exit on its own and
// returns its exit code (a non-zero code is not an error here). It sends
// nothing; the error is only for a process still running at timeout.
func (s *Screen) WaitExit(timeout time.Duration) (int, error) {
	select {
	case <-s.waitDone:
		return s.waitExit, nil
	case <-time.After(timeout):
		return -1, fmt.Errorf("screen.WaitExit: process still running after %s", timeout)
	}
}

// Exited reports whether the process has exited.
func (s *Screen) Exited() bool {
	select {
	case <-s.waitDone:
		return true
	default:
		return false
	}
}

// AltScreen reports whether the emulator is showing the alternate screen,
// i.e. whether the program entered it and has not left it.
func (s *Screen) AltScreen() bool {
	s.settle(time.Now().Add(200 * time.Millisecond))
	s.emuMu.Lock()
	defer s.emuMu.Unlock()
	return s.emu.IsAltScreen()
}

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
