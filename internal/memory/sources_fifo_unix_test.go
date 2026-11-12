//go:build !windows

package memory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Run the potentially blocking operation in a child so a regression cannot
// leave a goroutine stuck in open or hang the entire test suite.
func TestMemorySourcesRejectFIFO(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "source.pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"capture", "current", "current-worktree"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMemorySourceFIFOProbe$")
			cmd.Env = append(os.Environ(), "KILN_MEMORY_FIFO_PROBE="+operation, "KILN_MEMORY_FIFO_ROOT="+root)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("%s blocked on a FIFO instead of rejecting it", operation)
			}
			if err != nil {
				t.Fatalf("%s: %v\n%s", operation, err, output)
			}
		})
	}
}

func TestMemorySourceFIFOProbe(t *testing.T) {
	operation := os.Getenv("KILN_MEMORY_FIFO_PROBE")
	if operation == "" {
		t.Skip("child-process probe")
	}
	root := os.Getenv("KILN_MEMORY_FIFO_ROOT")
	// A valid digest avoids vacuous rejection by any future hash validation.
	sources := []Source{{Root: root, Path: filepath.Join(root, "source.pipe"), SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}}
	switch operation {
	case "capture":
		if _, err := CaptureSources(root, []string{"source.pipe"}); err == nil {
			t.Fatal("captured a nonregular source")
		}
	case "current":
		if SourcesCurrent(sources) {
			t.Fatal("accepted a nonregular source as current")
		}
	case "current-worktree":
		if SourcesCurrentAt(sources, root) {
			t.Fatal("accepted a nonregular worktree source as current")
		}
	default:
		t.Fatalf("unknown probe %q", operation)
	}
}
