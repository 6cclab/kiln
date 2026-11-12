//go:build !windows

package execenv

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func confinedTestEnv(t *testing.T, root string) *Env {
	t.Helper()
	env, err := New(root).CloneForConfinedFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.CloseConfinedFiles(); err != nil {
			t.Error(err)
		}
	})
	return env
}

func TestConfinedFilesRejectLinksAndGitAliases(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "safe"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(out, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"outside": out, "git": filepath.Join(root, ".git")} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(secret, filepath.Join(root, "safe", "hard")); err != nil {
		t.Fatal(err)
	}
	env := confinedTestEnv(t, root)
	for _, path := range []string{"outside/secret", "git/config", ".git/config"} {
		t.Run(path, func(t *testing.T) {
			if _, err := env.ReadFile(path); err == nil {
				t.Error("read succeeded")
			}
			if scanner, closer, err := env.OpenLineScanner(path, 1024); err == nil {
				_ = scanner
				if closeErr := closer.Close(); closeErr != nil {
					t.Error(closeErr)
				}
				t.Error("scanner opened")
			}
			if err := env.WriteFile(path, []byte("changed")); err == nil {
				t.Error("write succeeded")
			}
		})
	}
	if err := env.WriteFile("safe/hard", []byte("changed")); err == nil {
		t.Error("hardlink write succeeded")
	}
	if _, err := env.ReadFile("safe/hard"); err == nil {
		t.Error("hardlink read succeeded")
	}
	got, err := os.ReadFile(secret)
	if err != nil || string(got) != "secret" {
		t.Fatalf("outside changed: %q, %v", got, err)
	}
}

func TestConfinedFilesAncestorSwap(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	safe := filepath.Join(root, "switch")
	moved := filepath.Join(root, "moved")
	if err := os.Mkdir(safe, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(out, "file")
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	env := confinedTestEnv(t, root)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			if err := os.Rename(safe, moved); err != nil {
				continue
			}
			if err := os.Symlink(out, safe); err == nil {
				_ = os.Remove(safe)
			}
			_ = os.Rename(moved, safe)
		}
	}()
	for i := 0; i < 500; i++ {
		data, err := env.ReadFile("switch/file")
		if err == nil && string(data) == "secret" {
			t.Fatal("read escaped worktree")
		}
		_ = env.WriteFile("switch/file", []byte("inside"))
	}
	wg.Wait()
	got, err := os.ReadFile(secret)
	if err != nil || string(got) != "secret" {
		t.Fatalf("outside changed: %q, %v", got, err)
	}
}

func TestConfinedFilesCloneDoesNotCloseParent(t *testing.T) {
	root := t.TempDir()
	parent := confinedTestEnv(t, root)
	child, err := parent.CloneForConfinedFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.CloseConfinedFiles(); err != nil {
		t.Fatal(err)
	}
	if err := child.WriteFile("escaped", []byte("no")); err == nil {
		t.Fatal("closed scope wrote through ordinary Env")
	}
	if err := parent.WriteFile("file", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	got, err := parent.ReadFile("file")
	if err != nil || strings.TrimSpace(string(got)) != "ok" {
		t.Fatalf("parent read %q: %v", got, err)
	}
}

func TestConfinedParentDescriptorsCloseOnExec(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	env := confinedTestEnv(t, root)
	fd, _, err := env.confinedParent("nested/file", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Close(fd); err != nil {
			t.Error(err)
		}
	}()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("confined parent descriptor is inherited by child processes")
	}
}

func TestConfinedWritePreservesExistingModeUnderUmask(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	env := confinedTestEnv(t, root)
	old := unix.Umask(077)
	defer unix.Umask(old)
	if err := env.WriteFile("file", []byte("new")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0644 {
		t.Fatalf("existing mode = %04o, want 0644", got)
	}
	if err := env.WriteFile("fresh", []byte("new")); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(filepath.Join(root, "fresh"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Fatalf("new mode = %04o, want umask-adjusted 0600", got)
	}
}

func TestConfinedStatMatchesOrdinaryMetadata(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	// Choose a known nonzero mtime so a zero-filled result cannot pass.
	when := time.Unix(1_700_000_000, 123_000_000)
	for _, name := range []string{"file", "dir"} {
		if err := os.Chtimes(filepath.Join(root, name), when, when); err != nil {
			t.Fatal(err)
		}
	}
	ordinary, confined := New(root), confinedTestEnv(t, root)
	for _, name := range []string{"file", "dir", "link"} {
		t.Run(name, func(t *testing.T) {
			want, err := ordinary.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			got, err := confined.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("confined metadata = %+v, ordinary = %+v", got, want)
			}
		})
	}
	for _, name := range []string{"escape/secret", ".git/config", "../outside"} {
		if _, err := confined.Stat(name); err == nil {
			t.Errorf("Stat(%q) escaped confinement", name)
		}
	}
	if err := confined.CloseConfinedFiles(); err != nil {
		t.Fatal(err)
	}
	if _, err := confined.Stat("file"); err == nil {
		t.Fatal("Stat after Close succeeded")
	}
}

func TestConfinedFilesEnabledLifecycle(t *testing.T) {
	var nilEnv *Env
	if nilEnv.ConfinedFilesEnabled() {
		t.Fatal("nil Env reports confined")
	}
	env := New(t.TempDir())
	if env.ConfinedFilesEnabled() {
		t.Fatal("ordinary Env reports confined")
	}
	confined := confinedTestEnv(t, env.Cwd)
	if !confined.ConfinedFilesEnabled() {
		t.Fatal("open scope reports unconfined")
	}
	if err := confined.CloseConfinedFiles(); err != nil {
		t.Fatal(err)
	}
	if confined.ConfinedFilesEnabled() {
		t.Fatal("closed scope reports confined")
	}
}
