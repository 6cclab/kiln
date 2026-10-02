package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// kiln's status-line git status runs outside any sandbox, on a repository
// a sandboxed command may have written to. It must not run the
// repository's core.fsmonitor command (git status runs it, and a
// submodule's config can carry one). The same plain git status does run
// it, which shows the fixture exercises the hook.
func TestReadGitStatusIgnoresFsmonitor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(t.TempDir(), "fsmonitor.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644)
	git("add", "a.txt")
	git("-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "first")
	git("config", "core.fsmonitor", hook)
	os.WriteFile(filepath.Join(repo, "b.txt"), []byte("y"), 0o644)

	if _, ok := readGitStatusAt(context.Background(), repo); !ok {
		t.Fatal("git status failed")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("kiln's git status ran the repository's fsmonitor command")
	}

	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	_ = cmd.Run()
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git does not run core.fsmonitor on status; the check above proves nothing here")
	}
}
