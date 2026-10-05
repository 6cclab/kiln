//go:build windows

package execenv

// openNonblock is unused on Windows: there are no FIFOs to open, and
// os.OpenFile has no non-blocking flag there.
const openNonblock = 0
