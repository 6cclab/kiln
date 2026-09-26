//go:build e2e

package e2e

// TestTUI_Queue drives the real, PTY-attached harness TUI through the
// "queued follow-up" flow (docs/kiln-design-handoff/README.md: "Enter
// sends the message. If the agent is busy, it becomes a queued
// follow-up"): submit a message, type a second one while the first turn
// is still busy, and confirm the second one both renders as a "queued"
// `you` block right away and — once the first turn ends — actually
// reaches the model, without the person having to press Enter again.
//
// This is the end-to-end proof for internal/harness/lane.go's Steer/
// drainInbox mechanism (unit-level proof: internal/harness/steer_verify_test.go's
// TestSteerDeliveryWhileBusy) and reuses tui_test.go's own helpers
// (startTUI, tuiFixture, waitReady, waitTurnSettled) rather than
// redefining them.
//
// It is also the regression test for a real deadlock this feature had
// never been driven through before: Lane.Steer runs synchronously on the
// TUI's own Update goroutine (app.go's handleSubmit), and it used to emit
// harness.EventQueueUpdate the same way every other harness event does —
// but every other event is emitted from the lane's own background
// goroutine, so bridge.go's Wire handler calling the blocking
// Bridge.Send/tea.Program.Send from there is safe. Calling it from
// Steer's own (Update) goroutine self-deadlocked: tea.Program.Send blocks
// until the event loop's goroutine drains its message channel, and that
// goroutine was the one blocked inside Send. Fixed by routing that one
// event through Bridge.SendAsync (queues onto the bridge's own committer
// goroutine, the same one Commit already uses for Println).

import (
	"strings"
	"testing"
	"time"
)

func TestTUI_Queue(t *testing.T) {
	// The two "text" steps must land as two separate model turns, not
	// merge into one response (internal/testkit/faux/script.go's
	// flattenSteps only starts a new turn at a tool_call/disconnect_after/
	// error/on_tool_result boundary — two bare "text" steps in a row
	// would otherwise be sent back as a single reply). The on_tool_result
	// gate below never actually matches (this turn has no tool call), but
	// per that package's own contract a mismatch is only recorded, not
	// fatal — it still serves "Got the follow-up." as the second turn,
	// which is all this test needs from it.
	script := `
model: faux-1
steps:
  - text: "working on it"
    delay: 1500ms
  - on_tool_result: none
    then:
      - text: "Got the follow-up."
`
	proj, home, sessDir, addr, requests := tuiFixture(t, script)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "dontAsk",
	)
	waitReady(t, s)

	s.Send("first")
	s.SendKey("enter")

	// Wait for the busy spinner row, confirming the first turn's request
	// is in flight (and thus that a follow-up typed now is a genuine
	// mid-turn queue, not a race against a turn that already finished).
	if err := s.WaitFor("first", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if anyRowMatches(s, spinnerFramePattern) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("spinner never appeared after submitting the first message:\n%s", strings.Join(s.Rows(), "\n"))
		}
		time.Sleep(15 * time.Millisecond)
	}

	s.Send("second")
	s.SendKey("enter")

	// The queued follow-up commits its own "you ... queued" block right
	// away, before the first turn has even ended.
	if err := s.WaitFor("queued", 2*time.Second); err != nil {
		t.Fatalf("queued follow-up block never appeared:\n%s", strings.Join(s.Rows(), "\n"))
	}
	joined := strings.Join(s.Rows(), "\n")
	if !strings.Contains(joined, "second") {
		t.Fatalf("queued follow-up text %q not on screen:\n%s", "second", joined)
	}

	// Once the first turn ends, the queued follow-up must have been
	// driven automatically — no second Enter needed — and its reply
	// lands on screen.
	waitTurnSettled(t, s)
	if err := s.WaitFor("Got the follow-up.", 3*time.Second); err != nil {
		t.Fatalf("the queued follow-up's own reply never arrived:\n%s", strings.Join(s.Rows(), "\n"))
	}

	found := false
	for _, msgs := range requests() {
		if strings.Contains(string(msgs), "second") {
			found = true
			break
		}
	}
	if !found {
		t.Error("faux never received a request whose Messages contained the queued follow-up text")
	}
}
