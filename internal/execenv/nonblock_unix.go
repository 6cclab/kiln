//go:build !windows

package execenv

import "syscall"

// openNonblock keeps ReadFile's open from blocking on a FIFO swapped in
// after the regular-file check (openRegular); reads of a regular file are
// unaffected. Mirrors internal/gitfiles's own openNonblock.
const openNonblock = syscall.O_NONBLOCK
