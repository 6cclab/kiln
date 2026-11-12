//go:build !darwin && !linux

package sandbox

import "fmt"

func rejectWorkerHardlinks(string) error {
	return fmt.Errorf("sandbox: worker hardlink validation requires macOS or Linux")
}
