package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// within fails the test unless fn returns within half a second: a read of
// /dev/zero or a FIFO must end at once, not allocate or block. On a
// timeout it ends the whole test process, mirroring
// internal/gitfiles/hostile_test.go's own within (duplicated rather than
// exported across packages for a test-only helper).
func withinReadLimit(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		fmt.Fprintf(os.Stderr, "--- FAIL: %s: %s did not return within half a second; exiting so the read cannot keep allocating\n", t.Name(), what)
		os.Exit(1)
	}
}

// A regular file above MaxReadFileBytes is refused by its Stat size
// alone — ReadFile never opens an io.Reader over it, let alone reads it —
// proven with a sparse file (Truncate sets the reported size without
// writing any of those bytes to disk, so this test allocates and writes
// nothing close to the limit itself).
func TestReadFileRefusesOversizedWithoutReading(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxReadFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	env := New(dir)
	withinReadLimit(t, "ReadFile on an oversized sparse file", func() {
		_, err := env.ReadFile("huge.bin")
		var tooLarge *ErrFileTooLarge
		if !errors.As(err, &tooLarge) {
			t.Errorf("ReadFile err = %v, want *ErrFileTooLarge", err)
			return
		}
		if tooLarge.Size != MaxReadFileBytes+1 {
			t.Errorf("Size = %d, want %d", tooLarge.Size, MaxReadFileBytes+1)
		}
	})
}

func TestReadFileWithinCapSucceeds(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := New(dir)
	data, err := env.ReadFile("small.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q", data)
	}
}

func symlinkOrSkipReadLimit(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/zero or FIFOs on Windows")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
}

// A path that resolves to a device (a dropped-in /dev/zero, the same
// hostile shape internal/gitfiles tests against) is refused as "not a
// regular file" at once, never read.
func TestReadFileRefusesDevZero(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "zero")
	symlinkOrSkipReadLimit(t, "/dev/zero", link)

	env := New(dir)
	withinReadLimit(t, "ReadFile on a /dev/zero symlink", func() {
		if _, err := env.ReadFile("zero"); err == nil {
			t.Error("ReadFile on /dev/zero succeeded, want a refusal")
		}
	})
}

// A FIFO must never be opened blocking: nothing here ever writes to the
// other end, so a blocking open would hang this test forever.
func TestReadFileRefusesFIFOWithoutBlocking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pipe")
	if err := mkfifoForTest(path); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	env := New(dir)
	withinReadLimit(t, "ReadFile on a FIFO", func() {
		if _, err := env.ReadFile("pipe"); err == nil {
			t.Error("ReadFile on a FIFO succeeded, want a refusal")
		}
	})
}
