//go:build darwin || linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes r.ws a repository with one commit and a local remote,
// outside the sandbox.
func gitRepo(t *testing.T, r *realRig) (remote string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	remote = filepath.Join(r.outside, "remote.git")
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HOME="+r.home, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(r.outside, "init", "-q", "--bare", remote)
	run(r.ws, "init", "-q", "-b", "main")
	run(r.ws, "config", "user.email", "t@example.com")
	run(r.ws, "config", "user.name", "t")
	os.WriteFile(filepath.Join(r.ws, "a.txt"), []byte("one\n"), 0o644)
	run(r.ws, "add", "a.txt")
	run(r.ws, "commit", "-qm", "first")
	run(r.ws, "remote", "add", "origin", remote)
	run(r.ws, "push", "-q", "origin", "main")
	return remote
}

// The redirect attack: a sandboxed command writes a doctored git directory
// elsewhere in the workspace and points .git/commondir (or a linked
// worktree's commondir/gitdir) at it, or sets a filter through
// .git/info/attributes. Git run later outside the sandbox — kiln's own
// status line, the user's next commit — would run its hooks and filters.
// None of those writes may stick.
func TestRealSandboxGitDirRedirect(t *testing.T) {
	r := newRealRig(t, Config{}, nil)
	gitRepo(t, r)
	wt := filepath.Join(r.ws, ".git", "worktrees", "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(filepath.Join(wt, "commondir"), []byte("../..\n"), 0o644)

	for _, cmd := range []string{
		"mkdir -p evil/hooks && echo evil > evil/hooks/pre-commit; echo ../evil > .git/commondir",
		"echo /tmp/evil > .git/gitdir",
		"mkdir -p .git/info; echo 'a.txt filter=x' > .git/info/attributes",
		"echo ../../../evil > .git/worktrees/wt/commondir",
		"echo /tmp/evil/.git > .git/worktrees/wt/gitdir",
		"mkdir -p .git/modules/m && echo '[core]' > .git/modules/m/config",
		"echo '[core]' > .git/config.worktree",
		"mkdir -p h && echo evil > h/pre-commit && rm -rf .git/hooks; mv h .git/hooks",
	} {
		r.run(cmd) // each may fail; what matters is what is on disk after
	}
	if b, err := os.ReadFile(filepath.Join(r.ws, ".git", "commondir")); err == nil {
		t.Errorf(".git/commondir written: %q", b)
	}
	mustNotExist(t, filepath.Join(r.ws, ".git", "gitdir"))
	mustNotExist(t, filepath.Join(r.ws, ".git", "info", "attributes"))
	mustNotExist(t, filepath.Join(r.ws, ".git", "config.worktree"))
	mustNotExist(t, filepath.Join(r.ws, ".git", "modules", "m", "config"))
	mustNotExist(t, filepath.Join(r.ws, ".git", "hooks", "pre-commit"))
	if b, _ := os.ReadFile(filepath.Join(wt, "commondir")); strings.TrimSpace(string(b)) != "../.." {
		t.Errorf("worktree commondir changed: %q", b)
	}
	mustNotExist(t, filepath.Join(wt, "gitdir"))
}

// What git writes in ordinary work still goes through: commit, branch and
// checkout, stash, rebase, fetch from a remote outside the workspace, gc.
// (A push to a local-path remote outside the workspace writes there, so
// the sandbox refuses it, as it should.)
func TestRealSandboxGitWorkflow(t *testing.T) {
	r := newRealRig(t, Config{}, nil)
	gitRepo(t, r)
	steps := []string{
		"echo two >> a.txt && git commit -qam second",
		"git checkout -qb feature && echo f > f.txt && git add f.txt && git commit -qm feat",
		"git checkout -q main && echo three >> a.txt && git commit -qam third",
		"echo wip >> a.txt && git stash -q && git stash pop -q && git checkout -q -- a.txt",
		"git checkout -q feature && git rebase -q main",
		"git fetch -q origin && git log --oneline origin/main | wc -l",
		"git status --porcelain && git gc -q",
	}
	for _, s := range steps {
		if out, code := r.run(s); code != 0 {
			t.Fatalf("%s: exit %d\n%s", s, code, out)
		}
	}
	out, _ := r.run("git log --oneline | wc -l")
	if strings.TrimSpace(out) != "4" {
		t.Errorf("history after rebase = %q commits, want 4", strings.TrimSpace(out))
	}
}
