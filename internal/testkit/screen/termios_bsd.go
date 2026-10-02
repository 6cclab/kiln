//go:build darwin || freebsd || netbsd || openbsd

package screen

import "golang.org/x/sys/unix"

const ioctlGetTermios = unix.TIOCGETA
