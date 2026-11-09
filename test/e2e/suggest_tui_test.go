//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// suggestScript scripts two turns on the session model (faux-1): the first
// answers the user's prompt, the second is consumed by the prompt-suggestion
// call kiln fires after the turn completes (see chat.go's suggestNextPrompt).
// end_turn on the first step forces the turn boundary so the second text step
// answers the suggestion request rather than merging into the first reply.
const suggestScript = `model: faux-1
steps:
  - text: "Done — the fix is in place."
    usage: {input: 20, output: 8}
    end_turn: true
  - text: "run the tests"
    usage: {input: 12, output: 4}
`

// fgOfSubstring returns the foreground colour of the first cell of the first
// row that contains substr, and whether it was found. It is how the ghost's
// rendition is checked without hardcoding a hex (the faint token is
// contrast-adjusted against the terminal background at runtime, theme.go).
func fgOfSubstring(s *screen.Screen, substr string) (string, bool) {
	rows := s.Rows()
	styles := s.Styles()
	for y, row := range rows {
		x := strings.Index(row, substr)
		if x < 0 || y >= len(styles) || x >= len(styles[y]) {
			continue
		}
		return styles[y][x].Fg, true
	}
	return "", false
}

// TestTUI_PromptSuggestion_GhostAppearsAndIsDim drives the real binary with
// the prompt-suggestion toggle on, and proves the end-to-end wiring goldens
// and unit tests can't: a real model call (chat.go's suggestNextPrompt) ->
// msgSuggestion -> the dim ghost actually rendered in the input box. The
// editor's Tab-accept is unit-tested against the real key handler
// (internal/tui/editor/model_test.go); the colour is checked here because a
// dim/colour defect never shows in the plain-text rows (.claude/rules).
func TestTUI_PromptSuggestion_GhostAppearsAndIsDim(t *testing.T) {
	tuiExtraOpts = []screen.Option{screen.WithEnv("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION", "true")}
	t.Cleanup(func() { tuiExtraOpts = nil })

	proj, home, sessDir, addr, _ := tuiFixture(t, suggestScript)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	// The idle placeholder shows at startup, dim, in the same style the ghost
	// reuses — capture its foreground as the reference colour.
	placeholderFg, ok := fgOfSubstring(s, idlePlaceholderText)
	if !ok {
		t.Fatalf("startup placeholder %q not found:\n%s", idlePlaceholderText, strings.Join(s.Rows(), "\n"))
	}
	if placeholderFg == "" {
		t.Fatalf("startup placeholder has no explicit foreground; expected the dim faint token")
	}

	s.Send("fix the bug")
	s.SendKey("enter")
	if err := s.WaitFor("Done — the fix is in place.", 10*time.Second); err != nil {
		t.Fatalf("assistant reply never rendered: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}

	// The suggestion is a second, async model call made after the turn
	// settles: wait for its ghost to land in the box.
	if err := s.WaitFor("run the tests", 10*time.Second); err != nil {
		t.Fatalf("prompt-suggestion ghost never appeared: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}

	// The ghost must sit in the input box (below the transcript), not be
	// echoed into the committed transcript, and be rendered dim — the same
	// faint foreground as the placeholder it replaces.
	ghostFg, ok := fgOfSubstring(s, "run the tests")
	if !ok {
		t.Fatal("ghost text found by WaitFor but not locatable for a style check")
	}
	if ghostFg != placeholderFg {
		t.Errorf("ghost foreground = %q, want the dim placeholder colour %q", ghostFg, placeholderFg)
	}

	// The ghost is not committed history: the user never submitted it, so the
	// scrollback (committed, scrolled-off rows) must not contain it.
	if sb := strings.Join(s.Scrollback(), "\n"); strings.Contains(sb, "run the tests") {
		t.Errorf("ghost leaked into committed scrollback:\n%s", sb)
	}
}

// TestTUI_PromptSuggestion_OffByDefault is the negative control: with no
// toggle (kiln's default, matching Claude Code's interactive default), the
// same completed turn makes no suggestion call and shows no ghost. A faux
// script with only the single answering turn would be exhausted by any
// suggestion call, so an unexpected ghost would surface faux's
// "(faux: script exhausted)" text — which this test also guards against.
func TestTUI_PromptSuggestion_OffByDefault(t *testing.T) {
	const oneTurn = `model: faux-1
steps:
  - text: "Done — the fix is in place."
    usage: {input: 20, output: 8}
`
	proj, home, sessDir, addr, _ := tuiFixture(t, oneTurn)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("fix the bug")
	s.SendKey("enter")
	if err := s.WaitFor("Done — the fix is in place.", 10*time.Second); err != nil {
		t.Fatalf("assistant reply never rendered: %v", err)
	}
	waitTurnSettled(t, s)

	// Give any (erroneous) suggestion call time to land, then assert none did.
	if err := s.WaitQuiet(300*time.Millisecond, 3*time.Second); err != nil {
		t.Fatalf("screen never settled: %v", err)
	}
	joined := strings.Join(s.Rows(), "\n")
	if strings.Contains(joined, "faux: script exhausted") {
		t.Errorf("a suggestion call was made with the feature off (faux was over-consumed):\n%s", joined)
	}
	// The idle placeholder, not a ghost, occupies the empty box.
	if !strings.Contains(joined, idlePlaceholderText) {
		t.Errorf("idle placeholder missing; something replaced the empty-box slot:\n%s", joined)
	}
}
