//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestTUI_StartsInRepoWithCommits guards the startup deadlock found on
// 2026-09-24: in a repo where `git rev-parse --abbrev-ref HEAD` succeeds,
// the git status was sent to the program before its event loop ran, and
// Send blocked forever. Every other TUI test uses a repo with no commits,
// where the status read fails and nothing was sent.
func TestTUI_StartsInRepoWithCommits(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)
	if err := os.WriteFile(filepath.Join(proj, "README"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "README"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = proj
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	s := startTUI(t, 100, 24, proj, home, sessDir, addr)
	waitReady(t, s)
	// The branch name reaches the footer once the loop is running.
	if err := s.WaitFor("main", 5*time.Second); err != nil {
		if err2 := s.WaitFor("master", time.Second); err2 != nil {
			t.Fatalf("footer never showed the branch: %v", err)
		}
	}
	assertFooterInvariant(t, s)
}
