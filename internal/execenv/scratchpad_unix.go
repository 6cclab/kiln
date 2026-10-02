//go:build !windows

package execenv

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func defaultTempBase() string { return "/tmp" }

func tempDirName() string { return "kiln-" + strconv.Itoa(os.Getuid()) }

// checkPrivateDir accepts a real directory (not a symlink) owned by this
// user with no group or other permissions.
func checkPrivateDir(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory kiln created", p)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by another user", p)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is open to other users (%v)", p, fi.Mode().Perm())
	}
	return nil
}
