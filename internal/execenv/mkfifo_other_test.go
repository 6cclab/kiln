//go:build windows

package execenv

import "errors"

func mkfifoForTest(path string) error {
	return errors.New("FIFOs are not supported on Windows")
}
