package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSteerDelivery is a real-mechanism check for the TUI's queued
// follow-up feature (docs/kiln-design-handoff/README.md's "Queued
// follow-up"; app.go's handleSubmit calls Lane.Steer while a turn is
// busy). It does not drive the TUI — it isolates the one thing app.go
// cannot itself verify: whether Lane.Steer's queued text actually reaches
// the model on a later Prompt call, by inspecting the faux server's
// recorded request.
func TestSteerDelivery(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "first reply"
  - text: "second reply"
`, []string{"bash"})
	lane := rig.mustLane("main")

	if _, err := lane.Prompt(context.Background(), "first message", nil); err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}

	// Steer between turns — the harness-side approximation the plan
	// documents ("this phase only supports queuing between turns"), not
	// a mid-stream interrupt.
	if err := lane.Steer("queued follow-up text"); err != nil {
		t.Fatalf("Steer: %v", err)
	}

	if _, err := lane.Prompt(context.Background(), "second message", nil); err != nil {
		t.Fatalf("Prompt 2: %v", err)
	}

	reqs := rig.Faux.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Requests() len = %d, want 2", len(reqs))
	}
	// The second turn's request transcript should contain both the
	// steered text and the turn's own prompt, in that order (Steer's
	// entry becomes the branch's new tip before Prompt 2 sets its own
	// parent from GetTipID, so it should appear as a user message ahead
	// of "second message").
	body := string(reqs[1].Messages)
	if !containsAll(body, "queued follow-up text", "second message") {
		t.Fatalf("Steer text did not reach the model's second-turn request; got messages: %s", body)
	}

	// And the steered entry's own position: it must come before "second
	// message" in the transcript, or the queued text landed somewhere
	// other than as a prior turn in the conversation.
	var raw []json.RawMessage
	if err := json.Unmarshal(reqs[1].Messages, &raw); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	idxQueued, idxSecond := -1, -1
	for i, m := range raw {
		s := string(m)
		if idxQueued < 0 && containsAll(s, "queued follow-up text") {
			idxQueued = i
		}
		if idxSecond < 0 && containsAll(s, "second message") {
			idxSecond = i
		}
	}
	if idxQueued < 0 || idxSecond < 0 || idxQueued >= idxSecond {
		t.Fatalf("expected queued text before the second prompt in the transcript; idxQueued=%d idxSecond=%d, messages: %s", idxQueued, idxSecond, body)
	}
}

// TestSteerDeliveryWhileBusy is the scenario the TUI actually exercises
// (app.go's handleSubmit calls Lane.Steer only while m.busy — a follow-up
// typed WHILE the model is still streaming the current turn's response),
// as opposed to TestSteerDelivery's simpler between-turns case. It matters
// separately because the running turn's own branch-tip write (turn.go's
// EventMessageEnd handling, `SetValue(BranchTip(...), &responseEntryID)`)
// could in principle race with — and silently clobber — Steer's tip write
// if the running turn computed its response entry's parent before Steer
// ran. This test scripts a delay so Steer's call lands mid-turn, then
// asserts the queued text still reaches the model on the following turn.
func TestSteerDeliveryWhileBusy(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - delay: 200ms
  - text: "first reply"
  - text: "second reply"
`, []string{"bash"})
	lane := rig.mustLane("main")

	resultCh := make(chan RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := lane.Prompt(context.Background(), "first message", nil)
		resultCh <- r
		errCh <- err
	}()

	// Give Prompt time to reach the in-flight (delayed) request before
	// queuing, so Steer genuinely runs concurrently with the running turn
	// — not just before it starts.
	time.Sleep(50 * time.Millisecond)
	if err := lane.Steer("queued while busy"); err != nil {
		t.Fatalf("Steer (mid-turn): %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}
	if r := <-resultCh; r.Status != StatusCompleted {
		t.Fatalf("Prompt 1 status = %q, want completed", r.Status)
	}

	// The queued text must reach the model without any further explicit
	// Prompt call: the design ("Enter sends the message. If the agent is
	// busy, it becomes a queued follow-up") is that the in-flight turn's
	// own end-of-turn checkpoint (turn.go's drive(), the no-tool-calls
	// branch) notices the still-queued item and drives one more model
	// turn for it automatically. So by the time Prompt 1 has returned,
	// there must already be a second recorded request carrying the
	// queued text.
	reqs := rig.Faux.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Requests() len = %d, want 2 (the first-message turn plus the auto-continued queued turn)", len(reqs))
	}
	body := string(reqs[1].Messages)
	if !containsAll(body, "queued while busy") {
		t.Fatalf("mid-turn Steer text did not reach the model's follow-up request; got messages: %s", body)
	}

	// The lane must still be usable afterward: a fresh explicit Prompt
	// call starts its own new turn, on top of the queued turn's tip, and
	// is not confused by the auto-continue that just happened.
	if _, err := lane.Prompt(context.Background(), "second message", nil); err != nil {
		t.Fatalf("Prompt 2: %v", err)
	}
	reqs = rig.Faux.Requests()
	if len(reqs) != 3 {
		t.Fatalf("Requests() len = %d, want 3 after Prompt 2", len(reqs))
	}
	if !containsAll(string(reqs[2].Messages), "second message") {
		t.Fatalf("Prompt 2's own text did not reach the model; got messages: %s", string(reqs[2].Messages))
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
