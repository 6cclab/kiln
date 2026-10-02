//go:build !windows

package execenv

import (
	"os"
	"syscall"
)

// SharedFile reports an existing non-directory at p (not followed through
// a final symlink) with more than one hard link: the same file is also
// reachable by another name, anywhere on the volume, so where p sits says
// nothing about what writing it changes. A missing path or a directory is
// not shared.
func SharedFile(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || st.Nlink > 1
}
