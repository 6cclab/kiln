//go:build windows

package execenv

import (
	"fmt"
	"os"
)

// The system temp dir is already per-user on Windows.
func defaultTempBase() string { return os.TempDir() }

func tempDirName() string { return "kiln" }

func checkPrivateDir(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory kiln created", p)
	}
	return nil
}
