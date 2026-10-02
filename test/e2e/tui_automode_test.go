//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tuiAutoModeScript: three mutating commands in a row, each blocked by the
// classifier (faux-2, the "fast" role). The first two blocks go back to the
// model; the third is shown to the user as a prompt.
const tuiAutoModeScript = `models:
  faux-1:
    - tool_call: {name: bash, args: {command: "sh -c 'touch one'"}, id: b1}
    - on_tool_result: b1
      then:
        - tool_call: {name: bash, args: {command: "sh -c 'touch two'"}, id: b2}
    - on_tool_result: b2
      then:
        - tool_call: {name: bash, args: {command: "sh -c 'touch three'"}, id: b3}
    - on_tool_result: b3
      then:
        - text: "All set."
  faux-2:
    - text: '{"decision":"block","reason":"not part of the request"}'
      end_turn: true
    - text: '{"decision":"block","reason":"not part of the request"}'
      end_turn: true
    - text: '{"decision":"block","reason":"still not part of the request"}'
      end_turn: true
`

// TestTUI_AutoMode_BlocksThenPausesToAsk drives auto mode in the real TUI:
// a classifier block renders as the tool's block with "blocked by auto
// mode" in its meta; the third block in a row becomes a permission prompt,
// preceded by a system note naming the streak and the latest reason; and
// approving that prompt runs the command.
func TestTUI_AutoMode_BlocksThenPausesToAsk(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, tuiAutoModeScript)
	writeUserFastRole(t, home)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr, "--permission-mode", "auto")
	waitReady(t, s)

	s.Send("set up the project")
	s.SendKey("enter")

	if err := s.WaitFor("Allow kiln to run", 8*time.Second); err != nil {
		t.Fatalf("no prompt after the third block: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	joined := strings.Join(s.Rows(), "\n")
	if n := strings.Count(joined, "blocked by auto mode"); n != 2 {
		t.Errorf("%d tool blocks marked \"blocked by auto mode\", want 2:\n%s", n, joined)
	}
	// The note sits between the blocked calls and the prompt it explains.
	note := strings.Index(joined, "Auto mode paused: 3 actions in a row were blocked")
	lastBlock := strings.LastIndex(joined, "blocked by auto mode")
	prompt := strings.Index(joined, "approval needed")
	if note < 0 || !(lastBlock < note && note < prompt) {
		t.Errorf("want the note after the blocked calls and before the prompt (block %d, note %d, prompt %d):\n%s", lastBlock, note, prompt, joined)
	}
	// Auto mode is already on, so the prompt does not offer to switch to it.
	if strings.Contains(joined, "switch to auto mode") {
		t.Errorf("the prompt offers to switch to the mode already on:\n%s", joined)
	}
	s.SendKey("1")
	waitTurnSettled(t, s)

	for _, f := range []string{"one", "two"} {
		if _, err := os.Stat(filepath.Join(proj, f)); err == nil {
			t.Errorf("blocked command %q ran", f)
		}
	}
	if _, err := os.Stat(filepath.Join(proj, "three")); err != nil {
		t.Error("the command the user approved did not run")
	}
}
