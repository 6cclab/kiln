//go:build e2e

package e2e

// Phase 3's five PTY behaviour tests, left out of the phase that wired
// retry/esc/queue/declined behaviour into internal/harness and
// internal/tui (see plan mutable-booping-feigenbaum.md's Phase 3 gate and
// this suite's task brief). Drives the real, PTY-attached binary exactly
// like tui_test.go / tui_gap_test.go, reusing their helpers (startTUI,
// tuiFixture, waitReady, waitTurnSettled, waitQuiescent,
// assertGoldenStyles, loadFauxScript, spinnerRowPattern,
// assertFooterInvariant). Queued follow-up is out of scope here — the
// sibling agent working on internal/harness/lane.go, tui/app.go and
// test/e2e/tui_queue_test.go owns that behaviour and its test.
//
// Goldens are written under testdata/golden with a "tui-behaviour-"
// prefix, scoped to this file.

import (
	"strings"
	"testing"
	"time"
)

// --- Esc interrupts a running tool ----------------------------------------

// TestTUI_Esc_Interrupts drives testdata/faux/behaviour-esc.yaml (one
// `bash sleep 5` tool call) under bypassPermissions, waits for the busy
// line to show the tool actually running (bridge.go's Wire:
// busyLabelForToolStart gives bash "Running <command>", not a blanket
// "Running Bash" — finding 6), presses Esc, and checks finishTurn's
// StatusAborted path (app.go): the in-flight bash call commits as a red
// CallError block (RenderToolCall's "err" label-rule colour), followed by
// the "■ Interrupted. Tell kiln what to do instead." note, and the busy
// line is gone.
func TestTUI_Esc_Interrupts(t *testing.T) {
	script := loadFauxScript(t, "behaviour-esc")
	proj, home, sessDir, addr, _ := tuiFixture(t, script)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("run a slow command")
	s.SendKey("enter")

	if err := s.WaitFor("Running sleep 5", 3*time.Second); err != nil {
		t.Fatalf("never saw the busy line switch to \"Running sleep 5\": %v", err)
	}

	s.SendKey("esc")

	if err := s.WaitFor("Interrupted. Tell kiln what to do instead.", 5*time.Second); err != nil {
		t.Fatalf("never saw the interrupted note: %v", err)
	}
	if err := waitQuiescent(s, 200*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(s.Rows(), "\n")
	if !strings.Contains(joined, "sleep 5") {
		t.Errorf("interrupted bash call's block missing its command:\n%s", joined)
	}
	if anyRowMatches(s, spinnerRowPattern) {
		t.Error("busy line still present after Esc interrupted the turn")
	}
	assertFooterInvariant(t, s)

	// The "bash" label rule renders red (CallError's status colour) —
	// pinned by a styles golden since plain text can't tell red from any
	// other colour.
	assertGoldenStyles(t, s, "tui-behaviour-esc", stylesOpts{anchor: "sleep 5"})
}

// --- Permission declined (feedback) note -----------------------------------

// TestTUI_Permission_Declined_Note drives testdata/faux/bash-approve.yaml
// under manual permission mode and denies the Bash prompt with "4" — its
// rendered "No" (permission_render.go's RenderBashPermissionPrompt: Yes /
// don't-ask-again / switch-to-auto-mode / No), a bare deny with no
// feedback capture — and checks the resulting "✕ Declined <command>" note
// (permission_render.go's declinedNoteText: bash reads as just the
// command, no "Bash" prefix).
func TestTUI_Permission_Declined_Note(t *testing.T) {
	script := loadFauxScript(t, "bash-approve")
	proj, home, sessDir, addr, _ := tuiFixture(t, script)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "manual",
	)
	waitReady(t, s)

	s.Send("run the tests")
	s.SendKey("enter")

	if err := s.WaitFor("Allow kiln to run", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("4")

	if err := s.WaitFor("Declined npm test -- upload", 3*time.Second); err != nil {
		t.Fatalf("never saw the declined note: %v", err)
	}
	if err := waitQuiescent(s, 200*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(s.Rows(), "\n")
	if !strings.Contains(joined, "✕ Declined npm test -- upload") {
		t.Errorf("declined note missing its exact glyph/wording:\n%s", joined)
	}
}

// --- Retry countdown + reconnect ------------------------------------------

// TestTUI_Retry_CountdownAndReconnect drives testdata/faux/behaviour-
// retry.yaml (a text step cut by disconnect_after, then a clean one),
// waits for the live retry countdown block ("Retrying in Ns · attempt A
// of M · r to retry now", tui/retry.go's RenderRetry), presses "r" to cut
// the backoff short (Lane.RetryNow, keys bound in app.go), and checks the
// committed "↺ Reconnected on attempt 2" note lands and the turn finishes
// normally.
//
// The e2e suite runs with HARNESS_RETRY_JITTER=0 (startTUI), so the
// disconnect -> countdown -> auto-reconnect sequence always runs against
// the deterministic base backoff rather than a random delay.
func TestTUI_Retry_CountdownAndReconnect(t *testing.T) {
	script := loadFauxScript(t, "behaviour-retry")
	proj, home, sessDir, addr, _ := tuiFixture(t, script)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("go")
	s.SendKey("enter")

	if err := s.WaitFor("Stream interrupted", 5*time.Second); err != nil {
		t.Fatalf("never saw the retry block's red message line: %v", err)
	}
	if err := s.WaitFor("r to retry now", 2*time.Second); err != nil {
		t.Fatalf("never saw the retry countdown block: %v", err)
	}
	s.SendKey("r")

	if err := s.WaitFor("Reconnected on attempt", 8*time.Second); err != nil {
		t.Fatalf("never saw the reconnect note: %v", err)
	}

	waitTurnSettled(t, s)

	joined := strings.Join(s.Rows(), "\n")
	if !strings.Contains(joined, "Reconnected, continuing.") {
		t.Errorf("turn never completed with the post-retry text:\n%s", joined)
	}
}

// --- Streaming caret -----------------------------------------------------

// TestTUI_Streaming_Caret drives testdata/faux/behaviour-streaming.yaml
// (a long text step held back by a 150ms delay before any bytes stream,
// giving the turn a real busy window). It tries to catch the trailing
// amber caret (`▍`, app.go's renderStreamLive) mid-stream; if it cannot
// (faux writes every chunk of a scripted text back to back with no
// per-chunk pacing — confirmed by reading internal/testkit/faux/
// anthropic.go's streamAnthropic/chunkString, which chunks and flushes in
// a tight loop with no delay between chunks; the script's only delay
// happens once, before the first byte, not between chunks — so there is
// no way to script "slow enough to observe mid-stream" short of changing
// faux itself, which is out of this suite's scope), it says so rather
// than asserting something it did not actually verify, and falls back to
// asserting the committed text.
func TestTUI_Streaming_Caret(t *testing.T) {
	script := loadFauxScript(t, "behaviour-streaming")
	proj, home, sessDir, addr, _ := tuiFixture(t, script)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("stream something long")
	s.SendKey("enter")

	caretSeen := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(s.Rows(), "\n"), "▍") {
			caretSeen = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Logf("caret observed mid-stream: %v (see this test's doc comment: faux has no per-chunk pacing, so this is not expected to be reliable)", caretSeen)

	waitTurnSettled(t, s)
	joined := strings.Join(s.Rows(), "\n")
	if !strings.Contains(joined, "This is a long streamed reply") {
		t.Errorf("committed streamed text missing:\n%s", joined)
	}
	if strings.Contains(joined, "▍") {
		t.Error("caret still present after the turn settled")
	}
}

// --- Resize sweep mid-stream ------------------------------------------------

// TestTUI_ResizeSweep_MidStream drives testdata/faux/behaviour-
// streaming.yaml, resizes 120 -> 60 -> 40 while the turn is still busy
// (the delayed long text keeps it busy for a real, if narrow, window —
// see TestTUI_Streaming_Caret's doc comment on why "busy" is the
// reachable invariant here, not "caret visibly mid-stream"), and checks
// two invariants at each width: the viewport's row width matches the
// requested column count (screen.Screen enforces this itself via
// checkWidth, so this is really checking the resize was applied and
// rendered, not re-deriving the check), and, once the turn settles, no
// residual blank rows are left below the status line
// (OccupiedHeight() == len(Rows()), matching assertFooterInvariant's own
// check).
func TestTUI_ResizeSweep_MidStream(t *testing.T) {
	script := loadFauxScript(t, "behaviour-streaming")
	proj, home, sessDir, addr, _ := tuiFixture(t, script)

	s := startTUI(t, 120, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("stream something long")
	s.SendKey("enter")

	for _, w := range []int{120, 60, 40} {
		s.Resize(w, 30)
		time.Sleep(30 * time.Millisecond)
		// Viewport() enforces the "no row exceeds the terminal width"
		// invariant itself (screen.checkWidth, wired to t.Errorf since
		// this Screen carries a testing.TB) — calling it here is the
		// check.
		_ = s.Viewport()
	}

	waitTurnSettled(t, s)
	assertFooterInvariant(t, s)
}
