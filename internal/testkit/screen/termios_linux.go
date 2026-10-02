//go:build linux

package screen

import "golang.org/x/sys/unix"

const ioctlGetTermios = unix.TCGETS
