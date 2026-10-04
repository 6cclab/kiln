//go:build unix

package gitfiles

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO at HEAD would block a read forever; it reads as no repository.
func TestHostileFIFOHead(t *testing.T) {
	dir := fakeRepo(t)
	head := filepath.Join(dir, ".git", "HEAD")
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(head, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	within(t, "Find and Branch", func() {
		if r, ok := Find(dir); ok {
			if _, _, ok := r.Branch(); ok {
				t.Error("read a branch from a FIFO")
			}
		}
	})
}
