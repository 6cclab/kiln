//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	// The git branch/status row this test used to wait for has been
	// removed from the default UI (docs/claude-code-reference.md §1: no
	// status row at all any more; Claude Code only shows git through an
	// optional, separately-configured statusLine, not by default). Checked
	// in internal/tui/app.go's View: MsgGitStatus still updates
	// footer.State().Git (see internal/cli/tui.go's readGitStatus/
	// MsgGitStatus wiring), but nothing in View ever reads footer.State().Git
	// any more — the branch genuinely is not rendered. This test now only
	// guards the original regression it was written for: startup must not
	// deadlock in a repo that already has commits (waitReady must succeed).
	waitReady(t, s)
	assertFooterInvariant(t, s)
}
