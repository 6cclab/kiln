//go:build !unix

package settings

import (
	"os/exec"
	"testing"
	"time"
)

func ownProcessGroup(*exec.Cmd) {}

// waitProcessGroupGone has no process groups to inspect here; the
// differential test needs a real bash, which these platforms rarely have.
func waitProcessGroupGone(*testing.T, *exec.Cmd) { time.Sleep(50 * time.Millisecond) }
