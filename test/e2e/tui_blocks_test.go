//go:build e2e

package e2e

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// This file exercises Phase 2 of the kiln UI pass (transcript blocks: tool
// meta, plan, context) through the real, PTY-attached binary — see
// tui_test.go's own doc comment for the ground rules (screen assertions
// only, never a write-log). Reuses tuiFixture/startTUI/waitReady/
// waitTurnSettled from that file.

// TestTUI_PlanBlock_Live drives testdata/faux/plan-live.yaml: two
// todo_write calls in one turn, the second held back by a 600ms delay on
// the text that follows it, long enough for a PTY-driven poll to observe
// the live "plan" block's current-item marker (▸) move from the first
// item to the third before the turn settles. Once settled, the committed
// plan block should show the turn's final state (2 of 5 done), and the
// live copy should be gone (finishTurn clears m.todos after committing).
func TestTUI_PlanBlock_Live(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/plan-live.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, string(script))

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "dontAsk",
	)
	waitReady(t, s)

	s.Send("update the plan")
	s.SendKey("enter")

	// First todo_write: item one (read the upload handler) is in_progress.
	if err := s.WaitFor(regexp.MustCompile(`▸ read the upload handler`), 3*time.Second); err != nil {
		t.Fatalf("live plan never showed the first in-progress item: %v", err)
	}

	// Second todo_write moves the ▸ marker to item three; the 600ms delay
	// on the following text keeps this frame observable.
	if err := s.WaitFor(regexp.MustCompile(`▸ wire the retry loop into the route`), 3*time.Second); err != nil {
		t.Fatalf("live plan marker never moved to the third item: %v", err)
	}
	if err := s.WaitFor(regexp.MustCompile(`plan .*2/5`), 1*time.Second); err != nil {
		t.Fatalf("live plan label rule never showed 2/5 done: %v", err)
	}

	waitTurnSettled(t, s)

	// Committed final state: the plan block persists in scrollback at
	// 2/5 done (docs/kiln-design-handoff/README.md: "Blocks update in
	// place" — the last live state is the one that survives).
	joined := strings.Join(s.Rows(), "\n")
	if !regexp.MustCompile(`plan .*2/5`).MatchString(joined) {
		t.Errorf("committed plan block missing 2/5 done:\n%s", joined)
	}
	if !strings.Contains(joined, "wire the retry loop into the route") {
		t.Errorf("committed plan missing the in-progress item's text:\n%s", joined)
	}
}

// TestTUI_ToolMeta_Approved drives testdata/faux/bash-approve.yaml with
// permission mode "manual": the bash call prompts, answering "1" (Yes)
// approves it once, and the committed tool block's label-rule meta must
// read "approved · <elapsed>s" (bridge.go's toolMeta, EventToolEnd).
func TestTUI_ToolMeta_Approved(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/bash-approve.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, string(script))

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "manual",
	)
	waitReady(t, s)

	s.Send("run the tests")
	s.SendKey("enter")

	if err := s.WaitFor("Allow kiln to run", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("1")

	waitTurnSettled(t, s)

	joined := strings.Join(s.Rows(), "\n")
	// HARNESS_TEST_CLOCK is set by startTUI, so bridge.go's toolMeta
	// reports a deterministic "1.0s" elapsed segment.
	if !strings.Contains(joined, "approved · 1.0s") {
		t.Errorf("tool block meta missing \"approved · 1.0s\":\n%s", joined)
	}
}

// TestTUI_Context_Block drives a turn that reports usage (fixBugScript
// already sets usage on its closing text), then runs /context and asserts
// the structured "context" block renders: the label rule, the legend
// labels from commands.ContextBreakdown's segments, and a percentage
// column.
func TestTUI_Context_Block(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("fix the bug in math.js")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	s.Send("/context")
	s.SendKey("enter")

	if err := s.WaitFor(regexp.MustCompile(`context ─`), 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor("System prompt", 1*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor("Conversation", 1*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor(regexp.MustCompile(`\d+%`), 1*time.Second); err != nil {
		t.Fatal(err)
	}
}
