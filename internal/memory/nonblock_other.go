//go:build windows

package memory

// Windows has no Unix FIFO open flag; the regular-file checks still apply.
const openNonblock = 0
