//go:build !(darwin || freebsd || netbsd || openbsd || linux)

package screen

import "errors"

// RawMode is not supported here: there is no termios to read.
func (s *Screen) RawMode() (bool, error) {
	return false, errors.New("screen.RawMode: unsupported on this platform")
}
