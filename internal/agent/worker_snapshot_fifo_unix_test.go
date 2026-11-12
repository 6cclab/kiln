//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

func TestDirtyWorkerSnapshotExcludesUntrackedFIFO(t *testing.T) {
	repo := testRepo(t)
	if err := syscall.Mkfifo(filepath.Join(repo, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Lstat(filepath.Join(env.Cwd, "pipe")); !os.IsNotExist(err) {
		t.Fatal("special parent file appeared in worker")
	}
}
