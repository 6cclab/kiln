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
	"regexp"
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

// TestTUI_QueueTwo queues two follow-ups while the first turn is busy:
// the busy line counts both ("2 queued"), and once the turn ends both
// reach the model together in one request (Lane.Steer's inbox drains as
// a batch), answered by one reply.
func TestTUI_QueueTwo(t *testing.T) {
	script := `
model: faux-1
steps:
  - delay: 3s
  - text: "working on it"
    end_turn: true
  - text: "Got both follow-ups."
`
	proj, home, sessDir, addr, requests := tuiFixture(t, script)
	s := startTUI(t, 160, 60, proj, home, sessDir, addr, "--permission-mode", "dontAsk")
	waitReady(t, s)

	s.Send("first")
	s.SendKey("enter")
	deadline := time.Now().Add(3 * time.Second)
	for !anyRowMatches(s, spinnerFramePattern) {
		if time.Now().After(deadline) {
			t.Fatalf("spinner never appeared:\n%s", strings.Join(s.Rows(), "\n"))
		}
		time.Sleep(15 * time.Millisecond)
	}
	s.Send("second")
	s.SendKey("enter")
	if err := s.WaitFor("1 queued", 2*time.Second); err != nil {
		t.Fatalf("first follow-up not queued:\n%s", strings.Join(s.Rows(), "\n"))
	}
	s.Send("third")
	s.SendKey("enter")
	if err := s.WaitFor("2 queued", 2*time.Second); err != nil {
		t.Fatalf("second follow-up not queued:\n%s", strings.Join(s.Rows(), "\n"))
	}
	if err := s.WaitFor("Got both follow-ups.", 15*time.Second); err != nil {
		t.Fatalf("queued follow-ups never answered:\n%s", strings.Join(s.Rows(), "\n"))
	}
	reqs := requests()
	if len(reqs) != 2 {
		t.Fatalf("faux saw %d requests, want 2 (the turn, then one batch of follow-ups)", len(reqs))
	}
	last := string(reqs[1])
	if !strings.Contains(last, `"second"`) || !strings.Contains(last, `"third"`) {
		t.Errorf("second request lacks one of the follow-ups: %s", last)
	}
}

// TestTUI_QueueRestoredOnInterrupt: esc during a turn with a follow-up
// queued puts the follow-up back in the input instead of leaving it
// pending, so it does not ride along with the next thing the user says.
func TestTUI_QueueRestoredOnInterrupt(t *testing.T) {
	script := `
model: faux-1
steps:
  - text: "working on it"
    delay: 5s
  - on_tool_result: none
    then:
      - text: "Made done.txt."
`
	proj, home, sessDir, addr, requests := tuiFixture(t, script)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "dontAsk")
	waitReady(t, s)

	s.Send("first")
	s.SendKey("enter")
	deadline := time.Now().Add(3 * time.Second)
	for !anyRowMatches(s, spinnerFramePattern) {
		if time.Now().After(deadline) {
			t.Fatalf("spinner never appeared:\n%s", strings.Join(s.Rows(), "\n"))
		}
		time.Sleep(15 * time.Millisecond)
	}
	s.Send("stale follow-up")
	s.SendKey("enter")
	if err := s.WaitFor("queued", 2*time.Second); err != nil {
		t.Fatalf("queued block never appeared:\n%s", strings.Join(s.Rows(), "\n"))
	}

	s.SendKey("esc")
	if err := s.WaitFor("back in the input", 3*time.Second); err != nil {
		t.Fatalf("no note that the queued message was restored:\n%s", strings.Join(s.Rows(), "\n"))
	}
	rows := strings.Join(s.Rows(), "\n")
	if regexp.MustCompile(`─ +queued`).MatchString(rows) {
		t.Errorf("a queued tag is still on screen after the interrupt:\n%s", rows)
	}
	if !strings.Contains(rows, "› stale follow-up") && !strings.Contains(rows, "›  stale follow-up") {
		t.Errorf("the queued text is not in the input:\n%s", rows)
	}

	// Replace it with a different instruction: only that one is sent.
	for range len("stale follow-up") {
		s.SendKey("backspace")
	}
	s.Send("make done.txt")
	s.SendKey("enter")
	if err := s.WaitFor("Made done.txt.", 5*time.Second); err != nil {
		t.Fatalf("redirected turn never answered:\n%s", strings.Join(s.Rows(), "\n"))
	}
	for _, msgs := range requests() {
		if strings.Contains(string(msgs), "stale follow-up") {
			t.Errorf("the withdrawn follow-up still reached the model: %s", msgs)
		}
	}
}
