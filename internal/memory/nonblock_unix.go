//go:build !windows

package memory

import "syscall"

// openNonblock prevents a source replaced by a FIFO from blocking in open.
const openNonblock = syscall.O_NONBLOCK
