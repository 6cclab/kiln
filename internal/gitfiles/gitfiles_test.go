package gitfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// What the files say matches what git says, for a fresh repository, one
// with commits, a subdirectory, a linked worktree, a detached HEAD and a
// packed branch.
func TestFindAndBranchMatchGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := resolved(t, t.TempDir())
	run(t, repo, "init", "-q", "-b", "trunk")
	r, ok := Find(repo)
	if !ok || r.WorkTree != repo {
		t.Fatalf("Find(fresh) = %+v %v", r, ok)
	}
	if b, born, ok := r.Branch(); !ok || b != "trunk" || born {
		t.Errorf("fresh: branch %q born %v ok %v, want trunk, unborn", b, born, ok)
	}

	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "f")
	run(t, repo, "commit", "-qm", "one")
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ = Find(sub)
	if b, born, _ := r.Branch(); b != "trunk" || !born || r.WorkTree != repo {
		t.Errorf("subdir: %+v branch %q born %v", r, b, born)
	}

	wt := filepath.Join(resolved(t, t.TempDir()), "wt")
	run(t, repo, "worktree", "add", "-q", wt, "-b", "side")
	w, ok := Find(wt)
	if !ok || w.WorkTree != wt {
		t.Fatalf("Find(worktree) = %+v %v", w, ok)
	}
	want := run(t, wt, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(want) {
		want = filepath.Join(wt, want)
	}
	if resolved(t, w.CommonDir) != resolved(t, want) {
		t.Errorf("worktree common dir %q, git says %q", w.CommonDir, want)
	}
	if b, born, _ := w.Branch(); b != "side" || !born {
		t.Errorf("worktree branch %q born %v", b, born)
	}

	run(t, repo, "pack-refs", "--all")
	if b, born, _ := r.Branch(); b != "trunk" || !born {
		t.Errorf("packed: branch %q born %v", b, born)
	}
	run(t, repo, "checkout", "-q", "--detach")
	if b, _, _ := r.Branch(); b != run(t, repo, "rev-parse", "--abbrev-ref", "HEAD") {
		t.Errorf("detached: %q", b)
	}

	if _, ok := Find(t.TempDir()); ok {
		t.Error("Find found a repository in an empty directory")
	}
}
