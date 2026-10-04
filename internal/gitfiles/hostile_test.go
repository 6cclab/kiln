package gitfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// within fails the test unless fn returns within half a second: a read of
// /dev/zero or a FIFO must end at once, not allocate or block. On a
// timeout it ends the whole test process: fn's goroutine cannot be
// stopped, and an unbounded read of /dev/zero left running would take
// gigabytes of memory before the package's other tests finished.
func within(t *testing.T, what string, fn func()) {
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

// fakeRepo is a minimal repository on branch main, with one commit's ref.
func fakeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	for path, body := range map[string]string{
		filepath.Join(gitDir, "HEAD"):                  "ref: refs/heads/main\n",
		filepath.Join(gitDir, "refs", "heads", "main"): strings.Repeat("a", 40) + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/zero or FIFOs on Windows")
	}
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
}

// A repository delivered as an archive can make .git, HEAD, commondir or
// packed-refs a symlink to /dev/zero. Each reads as "not a repository"
// or "no such ref" at once, never as an endless read.
func TestHostileFilesToDevZero(t *testing.T) {
	t.Run(".git", func(t *testing.T) {
		dir := t.TempDir()
		symlinkOrSkip(t, "/dev/zero", filepath.Join(dir, ".git"))
		within(t, "Find", func() {
			if _, ok := Find(dir); ok {
				t.Error("found a repository")
			}
		})
	})
	t.Run("HEAD", func(t *testing.T) {
		dir := fakeRepo(t)
		symlinkOrSkip(t, "/dev/zero", filepath.Join(dir, ".git", "HEAD"))
		within(t, "Find and Branch", func() {
			if r, ok := Find(dir); ok {
				if _, _, ok := r.Branch(); ok {
					t.Error("read a branch from /dev/zero")
				}
			}
		})
	})
	t.Run("commondir", func(t *testing.T) {
		dir := fakeRepo(t)
		symlinkOrSkip(t, "/dev/zero", filepath.Join(dir, ".git", "commondir"))
		within(t, "Find", func() {
			if _, ok := Find(dir); ok {
				t.Error("found a repository through a /dev/zero commondir")
			}
		})
	})
	t.Run("packed-refs", func(t *testing.T) {
		dir := fakeRepo(t)
		if err := os.Remove(filepath.Join(dir, ".git", "refs", "heads", "main")); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, "/dev/zero", filepath.Join(dir, ".git", "packed-refs"))
		within(t, "Branch", func() {
			r, ok := Find(dir)
			if !ok {
				t.Fatal("not found")
			}
			if b, born, ok := r.Branch(); !ok || b != "main" || born {
				t.Errorf("branch %q born %v ok %v, want main, unborn", b, born, ok)
			}
		})
	})
}

// An oversized regular HEAD or .git file reads as nothing, not as a
// branch or a gitdir taken from its first bytes.
func TestHostileOversizedFiles(t *testing.T) {
	big := "ref: refs/heads/main" + strings.Repeat(" ", 1<<20) + "\n"

	dir := fakeRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	r, ok := Find(dir)
	if !ok {
		t.Fatal("not found")
	}
	if b, _, ok := r.Branch(); ok {
		t.Errorf("oversized HEAD read as branch %q", b)
	}

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(dir, ".git")+strings.Repeat(" ", 1<<20)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := Find(wt); ok {
		t.Error("oversized .git file read as a gitdir")
	}
}
