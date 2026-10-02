//go:build unix

package sandbox

import (
	"os"
	"syscall"
)

// ownedByMe reports whether fi belongs to the current user.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
