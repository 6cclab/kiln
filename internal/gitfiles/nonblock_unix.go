//go:build unix

package gitfiles

import "syscall"

// openNonblock keeps an open from blocking on a FIFO swapped in after the
// type check; reads of a regular file are unaffected.
const openNonblock = syscall.O_NONBLOCK
