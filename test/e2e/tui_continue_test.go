//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestTUI_Continue_ReplaysPriorTranscript drives a first turn against the
// real harness binary in print mode (fast, no PTY needed for that half),
// then starts the interactive TUI with --continue against the same
// HOME/session store/project and checks the resumed session's prior turn
// replays into the transcript.
//
// Root cause (before the fix): internal/cli/tui.go's RunInteractive found
// the resumed session fine (agent.ResumeIncomplete only finishes a prior
// run's *unfinished* operation, which there isn't one of here) but nothing
// ever called internal/tui.RenderTranscriptEntries for it at startup —
// only Ctrl+O's replayTranscript did, later, on demand. The screen showed
// the banner, then a large blank gap, then the input box — see qa/runs/c5/
// iterm-dark/startup-continue-resumes-120x40/01-continue-resumed.txt for
// the original capture (banner's closing rule on row 5, the input box's
// own top rule on row 37, nothing committed between them). The fix is
// app.go's Model.commitBanner: when cfg.IsResume, it calls
// m.replayTranscript() — the exact renderer Ctrl+O uses — right after the
// banner, once, at startup.
func TestTUI_Continue_ReplaysPriorTranscript(t *testing.T) {
	addr, _ := startFaux(t, loadFauxScript(t, "hello"))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)

	res := runHarness(t, proj, env, "-p", "add tests for the parser", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("seed run: exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	s := startTUI(t, 120, 40, proj, home, sessDir, addr, "--continue")
	waitReady(t, s)

	joined := strings.Join(s.Rows(), "\n")
	if !strings.Contains(joined, "add tests for the parser") {
		t.Errorf("resumed transcript missing the prior turn's user message:\n%s", joined)
	}
	if !strings.Contains(joined, "Hello from faux.") {
		t.Errorf("resumed transcript missing the prior turn's reply:\n%s", joined)
	}
	if strings.Contains(joined, "Recent sessions") {
		t.Errorf("a resumed session should not also show the \"Recent sessions\" block:\n%s", joined)
	}
}
