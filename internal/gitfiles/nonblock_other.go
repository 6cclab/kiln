//go:build !unix

package gitfiles

// openNonblock is unused where there are no FIFOs to open.
const openNonblock = 0
