package harness

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// Interrupting a reply mid-stream keeps what it had said, as Claude Code
// does: on screen (EventMessageEnd carries it), in the session (an aborted
// assistant entry holding the text) and in what the next request sends,
// and its tokens count in the session's usage. Before, the partial reply
// vanished from the screen and the conversation, and cost nothing.
func TestInterruptKeepsThePartialReply(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - chunk_delay: 30ms
    text: "`+strings.Repeat("The design has three parts. ", 12)+`"
    end_turn: true
  - text: "understood"
    end_turn: true
`, []string{"bash"})
	lane := rig.mustLane("main")

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	updates := 0
	var ended *msg.AssistantMessage
	rig.H.Events().On(EventMessageUpdate, func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		if ev.StreamEvent != nil && ev.StreamEvent.Type == msg.EventTextDelta {
			updates++
			if updates == 5 {
				cancel()
			}
		}
	})
	rig.H.Events().On(EventMessageEnd, func(ev Event) { ended = ev.Message })

	res, _ := lane.Prompt(ctx, "write the design doc", nil)
	if res.Status != StatusAborted {
		t.Fatalf("status %q, want aborted", res.Status)
	}
	if ended == nil || !strings.HasPrefix(msg.TextOf(ended.Content), "The design has") {
		t.Fatalf("message_end = %+v, want the partial reply", ended)
	}

	entries, _ := lane.FindEntries(context.Background())
	var kept *msg.AssistantMessage
	for _, e := range entries {
		if am, ok := e.Message.(msg.AssistantMessage); ok && e.Type == session.EntryMessage {
			kept = &am
			break
		}
	}
	if kept == nil || kept.StopReason != msg.StopAborted || !strings.HasPrefix(msg.TextOf(kept.Content), "The design has") {
		t.Fatalf("session's last assistant entry = %+v, want the partial reply marked aborted", kept)
	}
	if u := rig.H.Stats().Usage; u.Output == 0 {
		t.Fatalf("session usage %+v: the interrupted reply's tokens were not counted", u)
	}

	if _, err := lane.Prompt(context.Background(), "make it shorter", nil); err != nil {
		t.Fatal(err)
	}
	reqs := rig.Faux.Requests()
	if last := string(reqs[len(reqs)-1].Messages); !strings.Contains(last, "The design has three parts.") {
		t.Fatalf("the next request did not carry the partial reply: %s", last)
	}
}
