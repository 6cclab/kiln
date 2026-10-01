//go:build darwin

package execenv

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestKernelPath_OnlyFilesAndDirectories (verification of 12ce170, LOW 4):
// a device, FIFO or socket is never opened to ask its path; the canonical
// path then comes from its directory.
func TestKernelPath_OnlyFilesAndDirectories(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, "/dev/null", "/dev/zero"} {
		if got, ok := KernelPath(p); ok {
			t.Errorf("KernelPath(%s) opened it: %s", p, got)
		}
	}
	if got := CanonicalPath(fifo); got != fifo {
		t.Errorf("CanonicalPath(%s) = %s", fifo, got)
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := KernelPath(file); !ok || got != file {
		t.Errorf("KernelPath(%s) = %s, %v", file, got, ok)
	}
}
