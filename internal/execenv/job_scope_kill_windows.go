//go:build windows

package execenv

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

// taskkill is an external process on Windows; bound it to Close's deadline.
func killScopedGroup(pid int, deadline time.Time) error {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	return exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
