package settings

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Before an interactive trust dialog, kiln's own .kiln/settings.local.json
// is held even when untracked: telling kiln's file from a repository's
// takes git, and no git runs in a folder not yet trusted. Its deny rules
// apply; trusting the folder applies its allow rules.
func TestLoadSettings_UntrustedKilnLocalInteractive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	put(t, filepath.Join(cwd, ".kiln", "settings.local.json"), `{"permissions":{"allow":["Bash(curl *)"],"deny":["Bash(rm *)"]}}`)
	s := LoadSettings(cwd, LoadOptions{})
	if indexOf(s.Permissions.Allow, "Bash(curl *)") >= 0 || indexOf(s.HeldAllow, "Bash(curl *)") < 0 {
		t.Errorf("interactive, untrusted: allow %q held %q", s.Permissions.Allow, s.HeldAllow)
	}
	if indexOf(s.Permissions.Deny, "Bash(rm *)") < 0 {
		t.Errorf("deny not applied: %q", s.Permissions.Deny)
	}
	if s := LoadSettings(cwd, LoadOptions{Trusted: true}); indexOf(s.Permissions.Allow, "Bash(curl *)") < 0 {
		t.Errorf("trusted: allow not applied")
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newLocalRepo is a fresh repository holding an untracked
// .claude/settings.local.json; it returns the repository and the file.
func newLocalRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	p := filepath.Join(dir, ".claude", "settings.local.json")
	put(t, p, `{}`)
	return dir, p
}

// Under -p a local settings file applies only when it is shown to be the
// person's own: git says it is untracked, or that the folder is in no
// repository. A file tracked under another case, a file in a submodule, a
// broken repository and a missing git all hold it.
func TestRepoSupplied_FailsClosed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())

	untracked, up := newLocalRepo(t)
	if repoSupplied(untracked, up) {
		t.Error("untracked: held")
	}
	plain := t.TempDir()
	pp := filepath.Join(plain, ".claude", "settings.local.json")
	put(t, pp, `{}`)
	if repoSupplied(plain, pp) {
		t.Error("outside any repository: held")
	}

	otherCase, op := newLocalRepo(t)
	blob := gitIn(t, otherCase, "hash-object", "-w", op)
	gitIn(t, otherCase, "update-index", "--add", "--cacheinfo", "100644,"+blob+",.Claude/Settings.local.json")
	if !repoSupplied(otherCase, op) {
		t.Error("tracked as .Claude/Settings.local.json: not held")
	}

	// .claude is a submodule of the repository kiln runs in.
	sub, sp := newLocalRepo(t)
	inner := filepath.Join(sub, ".claude")
	gitIn(t, inner, "init", "-q")
	gitIn(t, inner, "add", "settings.local.json")
	gitIn(t, inner, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "x")
	head := gitIn(t, inner, "rev-parse", "HEAD")
	gitIn(t, sub, "update-index", "--add", "--cacheinfo", "160000,"+head+",.claude")
	if !repoSupplied(sub, sp) {
		t.Error(".claude a submodule: not held")
	}
	// The index's gitlink alone says so, with no .git under .claude.
	if err := os.RemoveAll(filepath.Join(inner, ".git")); err != nil {
		t.Fatal(err)
	}
	if !repoSupplied(sub, sp) {
		t.Error(".claude a gitlink in the index: not held")
	}
	// And a repository of its own at .claude, registered nowhere.
	nested, np := newLocalRepo(t)
	gitIn(t, filepath.Join(nested, ".claude"), "init", "-q")
	if !repoSupplied(nested, np) {
		t.Error(".claude a nested repository: not held")
	}

	broken, bp := newLocalRepo(t)
	if err := os.WriteFile(filepath.Join(broken, ".git", "index"), []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !repoSupplied(broken, bp) {
		t.Error("corrupt index: not held")
	}

	t.Setenv("PATH", "")
	if !repoSupplied(untracked, up) {
		t.Error("no git: not held")
	}
}
