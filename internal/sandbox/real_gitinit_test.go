//go:build darwin || linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A sandboxed command can create a repository (git init), and add and
// commit in it in the same line. What it writes into the new git
// directory that would run code when git is used outside the sandbox
// later does not survive: a hook under a name git runs, a config key such
// as core.hooksPath, core.fsmonitor or include.path. From the next command
// on, the directory is an existing one (config and hooks not writable).
// A background process the creating command left running cannot plant
// config for longer than until kiln's next command ends.
func TestRealSandboxGitInit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	r := newRealRig(t, Config{}, nil)
	pre := "export HOME=" + shellQuote(r.home) + " GIT_CONFIG_NOSYSTEM=1; "
	gitDir := filepath.Join(r.ws, ".git")
	outside := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = r.ws
		cmd.Env = append(os.Environ(), "HOME="+r.home, "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	fresh := func() {
		t.Helper()
		if err := os.RemoveAll(gitDir); err != nil {
			t.Fatal(err)
		}
		os.Remove(filepath.Join(r.ws, "PWNED"))
	}
	noHooks := func(t *testing.T) {
		t.Helper()
		entries, _ := os.ReadDir(filepath.Join(gitDir, "hooks"))
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".sample") {
				t.Errorf("hook left in the new repository: %s", e.Name())
			}
		}
		// The hook a commit would run outside the sandbox does not run.
		if out, err := outside("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-qm", "outside"); err != nil {
			t.Fatalf("commit outside the sandbox: %v %s", err, out)
		}
		mustNotExist(t, filepath.Join(r.ws, "PWNED"))
	}
	noKey := func(t *testing.T, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if out, err := outside("config", "--get-all", k); err == nil {
				t.Errorf("%s survived in the new repository: %q", k, out)
			}
		}
	}

	t.Run("init add commit", func(t *testing.T) {
		fresh()
		out, code := r.run(pre + "git init -q && echo a > a.txt && git add a.txt && git -c user.name=t -c user.email=t@example.com commit -qm first && echo committed")
		if code != 0 || !strings.Contains(out, "committed") {
			t.Fatalf("exit %d: %s", code, out)
		}
		if log, err := outside("log", "--oneline"); err != nil || len(strings.Split(log, "\n")) != 1 {
			t.Errorf("log after a sandboxed init and commit: %v %q", err, log)
		}
		if v, _ := outside("config", "--get", "core.repositoryformatversion"); v != "0" {
			t.Errorf("git init's own config lost: %q", v)
		}
	})

	t.Run("hook planted after init", func(t *testing.T) {
		for _, plant := range []string{
			`printf '#!/bin/sh\ntouch PWNED\n' > .git/hooks/pre-commit; chmod +x .git/hooks/pre-commit`,
			`printf '#!/bin/sh\ntouch PWNED\n' > .git/hooks/pre-commit.sample; chmod +x .git/hooks/pre-commit.sample; mv .git/hooks/pre-commit.sample .git/hooks/pre-commit`,
			`mkdir -p evil && printf '#!/bin/sh\ntouch PWNED\n' > evil/pre-commit && chmod +x evil/pre-commit && rm -rf .git/hooks && ln -s ../evil .git/hooks`,
		} {
			fresh()
			r.run(pre + "git init -q; " + plant)
			noHooks(t)
		}
	})

	t.Run(".git pointing elsewhere", func(t *testing.T) {
		for _, plant := range []string{
			`printf 'gitdir: evil\n' > .git`,
			`ln -s evil .git`,
		} {
			fresh()
			os.RemoveAll(filepath.Join(r.ws, "evil"))
			r.run(pre + `git init -q --bare evil && printf '#!/bin/sh\ntouch PWNED\n' > evil/hooks/pre-commit && chmod +x evil/hooks/pre-commit; ` + plant)
			if fi, err := os.Lstat(gitDir); err == nil && !fi.IsDir() {
				t.Errorf("%s: .git left as a %v", plant, fi.Mode().Type())
			}
			outside("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-qm", "outside")
			mustNotExist(t, filepath.Join(r.ws, "PWNED"))
		}
	})

	t.Run("config planted after init", func(t *testing.T) {
		fresh()
		r.run(pre + "git init -q && git config core.hooksPath evil && git config core.fsmonitor 'touch PWNED'")
		noKey(t, "core.hooksPath", "core.fsmonitor")
		fresh()
		r.run(pre + `git init -q && printf '[include]\n\tpath = /tmp/kiln-evil\n[core]\n\tfsmonitor = touch PWNED\n' >> .git/config`)
		noKey(t, "include.path", "core.fsmonitor")
		if _, err := outside("status", "--short"); err != nil {
			t.Fatalf("status outside: %v", err)
		}
		mustNotExist(t, filepath.Join(r.ws, "PWNED"))
	})

	t.Run("next command cannot write config", func(t *testing.T) {
		fresh()
		r.run(pre + "git init -q")
		r.run(pre + "git config core.hooksPath evil; printf '#!/bin/sh\ntouch PWNED\n' > .git/hooks/pre-commit")
		noKey(t, "core.hooksPath")
		noHooks(t)
	})

	t.Run("background process after the command", func(t *testing.T) {
		fresh()
		r.run(pre + "git init -q && (sleep 1; git config core.hooksPath evil) >/dev/null 2>&1 & disown")
		time.Sleep(2 * time.Second)
		r.run("true") // kiln's next command
		noKey(t, "core.hooksPath")
	})
}
