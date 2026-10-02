//go:build !windows

package execenv

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEnsureScratchpad_PrivatePerSessionDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("KILN_TMPDIR", base)
	t.Setenv("CLAUDE_CODE_TMPDIR", "")

	dir, err := EnsureScratchpad("/Users/me/my proj", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	realBase, _ := RealPath(base)
	want := filepath.Join(realBase, "kiln-"+strconv.Itoa(os.Getuid()), "-Users-me-my-proj", "sess-1", "scratchpad")
	if dir != want {
		t.Errorf("dir = %s, want %s", dir, want)
	}
	for p := dir; p != realBase; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s is %v, want 0700", p, fi.Mode().Perm())
		}
	}
	// A second session of the same project gets its own.
	other, err := EnsureScratchpad("/Users/me/my proj", "sess-2")
	if err != nil || other == dir {
		t.Errorf("second session: %s, %v", other, err)
	}
}

// In a shared temp dir someone else could create kiln's root first: a
// symlink there, or a directory open to others, is refused.
func TestEnsureScratchpad_RefusesPlantedRoot(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("KILN_TMPDIR", base)
		target := t.TempDir()
		if err := os.Symlink(target, TempRoot()); err != nil {
			t.Fatal(err)
		}
		if dir, err := EnsureScratchpad("/p", "s"); err == nil {
			t.Fatalf("accepted a symlinked root: %s", dir)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Error("wrote through the planted symlink")
		}
	})
	t.Run("open to others", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("KILN_TMPDIR", base)
		if err := os.Mkdir(TempRoot(), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(TempRoot(), 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureScratchpad("/p", "s"); err == nil || !strings.Contains(err.Error(), "open to other users") {
			t.Fatalf("err = %v, want a refusal", err)
		}
	})
}

func TestTempRoot_Overrides(t *testing.T) {
	uid := "kiln-" + strconv.Itoa(os.Getuid())
	t.Setenv("KILN_TMPDIR", "")
	t.Setenv("CLAUDE_CODE_TMPDIR", "")
	if got := TempRoot(); got != filepath.Join("/tmp", uid) {
		t.Errorf("default = %s", got)
	}
	t.Setenv("CLAUDE_CODE_TMPDIR", "/cc")
	if got := TempRoot(); got != filepath.Join("/cc", uid) {
		t.Errorf("CLAUDE_CODE_TMPDIR = %s", got)
	}
	t.Setenv("KILN_TMPDIR", "/k")
	if got := TempRoot(); got != filepath.Join("/k", uid) {
		t.Errorf("KILN_TMPDIR = %s", got)
	}
}
