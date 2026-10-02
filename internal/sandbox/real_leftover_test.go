//go:build darwin || linux

package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A process a sandboxed command leaves running (`nohup ... & disown`) is
// not killed when the command returns on macOS, as Claude Code leaves
// such processes running; it stays inside the sandbox it was started in.
// On Linux bwrap's PID namespace ends with the command, and it with it.
func TestRealSandboxLeftoverStaysConfined(t *testing.T) {
	r := newRealRig(t, Config{}, nil)
	mark := filepath.Join(r.ws, "alive")
	escaped := filepath.Join(r.outside, "escaped")
	out, code := r.run(`nohup sh -c 'sleep 1; touch ` + mark + `; touch ` + escaped + `' >/dev/null 2>&1 & disown; echo started`)
	if code != 0 || !strings.Contains(out, "started") {
		t.Fatalf("start: %d %q", code, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // the escape attempt follows the mark
	_, err := os.Stat(mark)
	switch runtime.GOOS {
	case "darwin":
		if err != nil {
			t.Fatalf("leftover process did not keep running (as Claude Code leaves it): %v", err)
		}
	case "linux":
		if err == nil {
			t.Error("leftover process outlived bwrap's PID namespace")
		}
	}
	mustNotExist(t, escaped)
}
