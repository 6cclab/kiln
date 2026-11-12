//go:build !darwin

package sandbox

import "fmt"

func rejectWorkerHardlinks(string) error {
	return fmt.Errorf("sandbox: worker hardlink validation requires macOS")
}
