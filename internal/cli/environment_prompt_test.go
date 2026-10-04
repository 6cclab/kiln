package cli

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestEnvironmentPrompt: the model is told its working directory, repo,
// platform and date. It used to get none of it and guessed paths
// (qa/findings *system-prompt-has-no-environment).
func TestEnvironmentPrompt(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	plain := t.TempDir()
	got := environmentPrompt(context.Background(), plain, now)
	for _, want := range []string{"Working directory: " + plain, "Is a git repository: no", "Platform: " + runtime.GOOS + "/" + runtime.GOARCH, "Today's date: 2026-09-27"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	if got := environmentPrompt(context.Background(), repo, now); !strings.Contains(got, "Is a git repository: yes (no commits yet)") {
		t.Errorf("fresh repo:\n%s", got)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("commit", "-q", "--allow-empty", "-m", "x")
	if got := environmentPrompt(context.Background(), repo, now); !strings.Contains(got, "Is a git repository: yes (branch main)") {
		t.Errorf("repo with a commit:\n%s", got)
	}
	// Built before the folder is trusted, it reads .git's files and runs no
	// program: with nothing on PATH it still knows the repository.
	t.Setenv("PATH", "")
	if got := environmentPrompt(context.Background(), repo, now); !strings.Contains(got, "Is a git repository: yes (branch main)") {
		t.Errorf("repo, no programs on PATH:\n%s", got)
	}
}
