//go:build e2e

package e2e

// Regression coverage for three transcript defects found by the qa
// campaign (qa/findings/20260926T232657Z-verbose-toggle-inconsistent.json,
// qa/findings/20260926T232657Z-thinking-invisible.json,
// qa/findings/20260926T232249Z-queued-block-order.json):
//
//  1. Ctrl+O used to render a committed tool block three different ways
//     (the live commit, replay.go's verbose replay, and replay.go's
//     non-verbose "grouped" replay), losing detail rather than gaining it
//     and dropping a blank row along the way. Fixed by unifying every
//     committed-tool-block render (live and replay) on one path
//     (replay.go no longer groups a committed replay, no longer overrides
//     Read's summary, and always leads with a blank row — see replay.go
//     and transcript.go's RenderToolCall/RenderTranscriptEntries).
//  2. A thinking block never appeared on screen in any state — nothing
//     ever set ThinkingView.Expanded true on a committed block. Fixed by
//     RenderThinking always showing a "thinking" label-rule row (collapsed
//     summary + "ctrl+o to expand" hint, or the full text when expanded).
//  3. A follow-up queued while a turn was busy committed to the transcript
//     immediately, landing above the reply to the turn it interrupted, and
//     kept its "queued" meta forever. Fixed by holding it in the live
//     region (m.queued, RenderQueuedFollowUp) until the lane's own drain
//     (MsgQueue{Len:0}) commits it for real, in order, after that reply.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// verboseToggleProject is scratchProject plus a package.json, so a faux
// "read" tool call has a real, small file to show a content preview from
// (matching the qa finding's own scenario, qa/scenarios/content/
// verbose-toggle.steps and testdata/faux/qa/content-verbose.yaml).
func verboseToggleProject(t *testing.T) string {
	t.Helper()
	proj := scratchProject(t)
	pkg := "{\n  \"name\": \"design-handoff-fixture\",\n  \"version\": \"0.0.0\",\n  \"private\": true,\n  \"dependencies\": {\n    \"left-pad\": \"^1.3.0\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(proj, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	return proj
}

const verboseToggleScript = `model: faux-1
steps:
  - text: "I'll check the project's package.json first."
  - tool_call: {name: read, args: {path: package.json}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "It's a small Node project with one dependency."
        usage: {input: 60, output: 30}
`

// TestTUI_VerboseToggle_ExpandsInPlace is the regression test for defect
// 20260926T232657Z-verbose-toggle-inconsistent. Before the fix this failed
// two ways: verbose mode showed a bare "Read 9 lines" line-count instead of
// the file content already visible collapsed (less detail, not more), and
// toggling back a second time showed "Read 1 file" — a third, grouped
// rendering never used by the live commit path at all.
func TestTUI_VerboseToggle_ExpandsInPlace(t *testing.T) {
	proj := verboseToggleProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, verboseToggleScript)

	s := startTUI(t, 120, 40, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("what dependencies does this project have")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	beforeRows := s.Rows()
	before := strings.Join(beforeRows, "\n")
	if !strings.Contains(before, "read ─") || !strings.Contains(before, "package.json") {
		t.Fatalf("collapsed transcript missing the read tool block:\n%s", before)
	}
	if !strings.Contains(before, "name") {
		t.Errorf("collapsed transcript should show a content preview (\"name\" from package.json), not just a line count:\n%s", before)
	}
	if !strings.Contains(before, "ctrl+o to expand") {
		t.Errorf("collapsed transcript missing the \"ctrl+o to expand\" hint:\n%s", before)
	}
	if strings.Contains(before, "Read 1 file") || strings.Contains(before, "Read 9 lines") {
		t.Errorf("collapsed transcript must not show a grouped/line-count summary:\n%s", before)
	}
	// The blank row between the `you` block and whatever follows it must
	// survive — defect 20260926T232657Z-verbose-toggle-inconsistent's
	// "the you block loses its blank row before the kiln label".
	assertBlankRowAfter(t, beforeRows, "what dependencies does this project have")

	s.SendKey("ctrl+o")
	if err := s.WaitFor("Showing detailed transcript", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	verboseRows := s.Rows()
	verbose := strings.Join(verboseRows, "\n")
	if !strings.Contains(verbose, "name") || !strings.Contains(verbose, "left-pad") {
		t.Errorf("verbose transcript should show the whole file content, got:\n%s", verbose)
	}
	if strings.Contains(verbose, "Read 9 lines") {
		t.Errorf("verbose transcript must not fall back to a bare line-count summary:\n%s", verbose)
	}
	if strings.Contains(verbose, "Read 1 file") {
		t.Errorf("verbose transcript must not show a grouped summary:\n%s", verbose)
	}
	// No stray per-message time/model row of its own (defect's "stray
	// '7:00 PM faux/faux-1' row" — dropped outright, see transcript.go's
	// RenderVerboseModelRow removal note).
	// (The banner, redrawn after the clear, names the model too; the
	// stray row was a time followed by the model.)
	if regexp.MustCompile(`\d{1,2}:\d{2} ?[AP]M +faux/faux-1`).MatchString(verbose) {
		t.Errorf("verbose transcript must not show a stray per-message model row:\n%s", verbose)
	}
	assertBlankRowAfter(t, verboseRows, "what dependencies does this project have")

	s.SendKey("ctrl+o")
	if err := s.WaitFor(tuiUserMark, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitQuiescent(s, 150*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	afterRows := s.Rows()
	after := strings.Join(afterRows, "\n")
	if strings.Contains(after, "Read 1 file") || strings.Contains(after, "Read 9 lines") {
		t.Errorf("toggling back must not land on a third, grouped rendering:\n%s", after)
	}
	if !strings.Contains(after, "ctrl+o to expand") {
		t.Errorf("toggling back must restore the collapsed hint:\n%s", after)
	}
	if !strings.Contains(after, "name") {
		t.Errorf("toggling back must restore the same content preview as the original collapsed block:\n%s", after)
	}
	assertBlankRowAfter(t, afterRows, "what dependencies does this project have")
}

// assertBlankRowAfter finds the first row containing marker and requires
// the very next row to be blank — the "one blank row between blocks" rule
// (docs/kiln-design-handoff/README.md "Block anatomy") that defect
// 20260926T232657Z-verbose-toggle-inconsistent's replay path used to drop.
// It uses the LAST occurrence of marker, not the first: a short transcript
// can still have an earlier, stale full-transcript reprint sharing the same
// viewport with the current one (see latestReplayFrom's doc comment) — the
// current toggle's own "you" block is always the last one on screen.
func assertBlankRowAfter(t *testing.T, rows []string, marker string) {
	t.Helper()
	last := -1
	for i, r := range rows {
		if strings.Contains(r, marker) {
			last = i
		}
	}
	if last < 0 {
		t.Fatalf("no row contains %q:\n%s", marker, strings.Join(rows, "\n"))
	}
	if last+1 >= len(rows) {
		t.Fatalf("row containing %q is the last row, nothing follows it", marker)
	}
	if strings.TrimRight(rows[last+1], " ") != "" {
		t.Errorf("row after %q = %q, want a blank row", marker, rows[last+1])
	}
}

const thinkingScript = `model: faux-1
steps:
  - thinking: "The limiter needs a fixed window, not a sliding one, since the test mock only implements zremrangebyscore."
  - text: "I'll use a fixed window counter."
    usage: {input: 20, output: 24}
`

// TestTUI_ThinkingBlock_CollapsedExpandedToggle is the regression test for
// defect 20260926T232657Z-thinking-invisible: a thinking block used to
// render nothing at all, in every state, because nothing ever set
// ThinkingView.Expanded true on a committed block.
func TestTUI_ThinkingBlock_CollapsedExpandedToggle(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, thinkingScript)

	s := startTUI(t, 120, 40, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("explain the change")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	collapsed := strings.Join(s.Rows(), "\n")
	if !strings.Contains(collapsed, "thinking") {
		t.Fatalf("collapsed transcript missing a \"thinking\" label rule row at all:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "∴") {
		t.Errorf("thinking block must not use the ∴ glyph (not in the design's glyph set):\n%s", collapsed)
	}
	if strings.Contains(collapsed, "zremrangebyscore") {
		t.Errorf("collapsed thinking must not show the full reasoning text yet:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "ctrl+o to expand") {
		t.Errorf("collapsed thinking missing the \"ctrl+o to expand\" hint:\n%s", collapsed)
	}

	s.SendKey("ctrl+o")
	if err := s.WaitFor("Showing detailed transcript", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	expanded := strings.Join(s.Rows(), "\n")
	if !strings.Contains(expanded, "zremrangebyscore") {
		t.Errorf("ctrl+o must expand the thinking block to its full reasoning text:\n%s", expanded)
	}

	s.SendKey("ctrl+o")
	if err := s.WaitFor(tuiUserMark, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitQuiescent(s, 150*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	// Each ctrl+o reprints the whole transcript from the session log
	// (replayTranscript's own doc comment: "Committed rows live in
	// scrollback and cannot be repainted"), so a short transcript like this
	// one's third full reprint can still share the 40-row viewport with the
	// tail of the *previous* (verbose) reprint above it — latestReplayFrom
	// drops everything before the last "you" block so the assertions below
	// read only the current toggle's own copy, not a stale one still
	// sitting higher up the same screen.
	collapsedAgain := latestReplayFrom(s.Rows(), "explain the change")
	if strings.Contains(collapsedAgain, "zremrangebyscore") {
		t.Errorf("toggling back must collapse the thinking block again:\n%s", collapsedAgain)
	}
	if !strings.Contains(collapsedAgain, "thinking") || !strings.Contains(collapsedAgain, "ctrl+o to expand") {
		t.Errorf("toggling back must restore the collapsed thinking row:\n%s", collapsedAgain)
	}
}

// latestReplayFrom returns rows from the last occurrence of marker onward,
// joined — the most recent of possibly several stacked full-transcript
// reprints still sharing one viewport (see the doc comment at this
// function's one call site).
func latestReplayFrom(rows []string, marker string) string {
	last := -1
	for i, r := range rows {
		if strings.Contains(r, marker) {
			last = i
		}
	}
	if last < 0 {
		return strings.Join(rows, "\n")
	}
	return strings.Join(rows[last:], "\n")
}

// TestTUI_QueueTwo_OrderAfterDrain is the regression test for defect
// 20260926T232249Z-queued-block-order: two follow-ups queued while a turn
// is busy used to commit to the transcript immediately (with a "queued"
// meta that never cleared), landing above the reply to the turn they
// interrupted. This reuses tui_queue_test.go's TestTUI_QueueTwo script and
// flow, adding the ordering assertion that test doesn't make.
func TestTUI_QueueTwo_OrderAfterDrain(t *testing.T) {
	script := `
model: faux-1
steps:
  - delay: 3s
  - text: "Working on the upload route now."
    end_turn: true
  - text: "Checked the login route and updated the tests."
`
	proj, home, sessDir, addr, requests := tuiFixture(t, script)
	s := startTUI(t, 160, 60, proj, home, sessDir, addr, "--permission-mode", "dontAsk")
	waitReady(t, s)

	s.Send("start the rate limiter work")
	s.SendKey("enter")
	deadline := time.Now().Add(3 * time.Second)
	for !anyRowMatches(s, spinnerFramePattern) {
		if time.Now().After(deadline) {
			t.Fatalf("spinner never appeared:\n%s", strings.Join(s.Rows(), "\n"))
		}
		time.Sleep(15 * time.Millisecond)
	}

	s.Send("also check the login route")
	s.SendKey("enter")
	if err := s.WaitFor("1 queued", 2*time.Second); err != nil {
		t.Fatalf("first follow-up not queued:\n%s", strings.Join(s.Rows(), "\n"))
	}
	// While queued, still visible (the live region, not the transcript —
	// liveTail's RenderQueuedFollowUp).
	if err := s.WaitFor("also check the login route", 2*time.Second); err != nil {
		t.Fatalf("queued follow-up text not visible while pending:\n%s", strings.Join(s.Rows(), "\n"))
	}

	s.Send("and update the tests too")
	s.SendKey("enter")
	if err := s.WaitFor("2 queued", 2*time.Second); err != nil {
		t.Fatalf("second follow-up not queued:\n%s", strings.Join(s.Rows(), "\n"))
	}

	if err := s.WaitFor("Checked the login route and updated the tests.", 15*time.Second); err != nil {
		t.Fatalf("queued follow-ups never answered:\n%s", strings.Join(s.Rows(), "\n"))
	}
	waitTurnSettled(t, s)

	rows := s.Rows()
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "queued") {
		t.Errorf("no row should still carry \"queued\" once the lane has drained:\n%s", joined)
	}

	idxFirstReply := indexOfSubstring(rows, "Working on the upload route now.")
	idxSecond := indexOfSubstring(rows, "also check the login route")
	idxThird := indexOfSubstring(rows, "and update the tests too")
	idxFinalReply := indexOfSubstring(rows, "Checked the login route and updated the tests.")
	if idxFirstReply < 0 || idxSecond < 0 || idxThird < 0 || idxFinalReply < 0 {
		t.Fatalf("one of the expected rows is missing entirely:\n%s", joined)
	}
	// The reply to the turn the follow-ups interrupted must come before
	// them — the out-of-order bug this defect reported.
	if !(idxFirstReply < idxSecond && idxSecond < idxThird && idxThird < idxFinalReply) {
		t.Errorf("transcript order wrong: first reply=%d, second=%d, third=%d, final reply=%d — want strictly increasing",
			idxFirstReply, idxSecond, idxThird, idxFinalReply)
	}

	reqs := requests()
	if len(reqs) != 2 {
		t.Fatalf("faux saw %d requests, want 2 (the turn, then one batch of follow-ups)", len(reqs))
	}
}

// indexOfSubstring returns the index of the first row containing substr,
// or -1.
func indexOfSubstring(rows []string, substr string) int {
	for i, r := range rows {
		if strings.Contains(r, substr) {
			return i
		}
	}
	return -1
}

const bashFailSixLinesScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo one; echo two; echo three; echo four; echo five; echo six; exit 3"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "That command failed."
        usage: {input: 200, output: 20}
`

// TestTUI_BashFailure_ExitStatusSurvivesCollapse is the regression test for
// defect 20260926T235921Z-bash-exit-code-message-hidden-by-collapse: a
// failed bash call's own "Command exited with code N" line is always the
// LAST line of its result, and the collapsed view used to keep only the
// FIRST collapsedResultLines(3) lines — so any failure with more than 2
// lines of its own output buried the exit status behind "… +N lines
// (ctrl+o to expand)" entirely. Six lines of output (well past the 3-line
// budget) makes sure the fix (clipResultLines) is really exercised, not
// just barely avoided.
func TestTUI_BashFailure_ExitStatusSurvivesCollapse(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, bashFailSixLinesScript)

	s := startTUI(t, 120, 40, proj, home, sessDir, addr,
		"--permission-mode", "dontAsk",
	)
	waitReady(t, s)

	s.Send("break something")
	s.SendKey("enter")
	if err := s.WaitFor("ctrl+o to expand", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, s)

	collapsed := strings.Join(s.Rows(), "\n")
	if !strings.Contains(collapsed, "one") {
		t.Fatalf("collapsed transcript missing the bash call's own output at all:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "Command exited with code 3") {
		t.Errorf("collapsed transcript must still show the exit-code line despite the 3-line budget:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "ctrl+o to expand") {
		t.Errorf("collapsed transcript missing the \"ctrl+o to expand\" hint (some lines are genuinely hidden):\n%s", collapsed)
	}

	s.SendKey("ctrl+o")
	if err := s.WaitFor("Showing detailed transcript", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	expanded := strings.Join(s.Rows(), "\n")
	for _, want := range []string{"one", "two", "three", "four", "five", "six", "Command exited with code 3"} {
		if !strings.Contains(expanded, want) {
			t.Errorf("expanded transcript missing %q:\n%s", want, expanded)
		}
	}
}
