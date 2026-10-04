//go:build !unix

package sandbox

import "os"

// ownedByMe: no sandbox runs on these platforms, so the temp directory is
// never created; report ownership rather than fail a check that cannot
// apply.
func ownedByMe(os.FileInfo) bool { return true }
