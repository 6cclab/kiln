package tui

import (
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fakeSink is a bridge sink that records Println/Send calls in order,
// standing in for a *tea.Program: a real Program needs a running event
// loop, which the queue-ordering property this test checks has nothing to
// do with. Guarded by a mutex since Bridge's committer goroutine writes to
// it while the test goroutine reads.
type fakeSink struct {
	mu      sync.Mutex
	printed []string
	sent    []tea.Msg
}

func (f *fakeSink) Println(args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 1 {
		if s, ok := args[0].(string); ok {
			f.printed = append(f.printed, s)
			return
		}
	}
	f.printed = append(f.printed, "")
}

func (f *fakeSink) Send(msg tea.Msg) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
}

func (f *fakeSink) snapshot() (printed []string, sent []tea.Msg) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.printed...), append([]tea.Msg(nil), f.sent...)
}

// TestBridge_FullscreenSinkOrdered checks Bridge's commit queue is ordered
// end-to-end across a mode flip: a Commit enqueued before SetFullscreen(true)
// still lands on the old sink (Println), and one enqueued after lands on the
// new one (Send(MsgTranscriptAppend)) — bridgeItem's own doc comment on the
// ordering guarantee.
func TestBridge_FullscreenSinkOrdered(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()

	f := &fakeSink{}
	b.setSink(f)

	b.Commit([]string{"A"})
	b.SetFullscreen(true)
	b.Commit([]string{"B"})

	var printed []string
	var sent []tea.Msg
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		printed, sent = f.snapshot()
		if len(printed) >= 1 && len(sent) >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	if len(printed) != 1 || printed[0] != "A" {
		t.Fatalf("printed = %v, want [\"A\"]", printed)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %v, want exactly one message", sent)
	}
	appended, ok := sent[0].(MsgTranscriptAppend)
	if !ok {
		t.Fatalf("sent[0] = %T, want MsgTranscriptAppend", sent[0])
	}
	if appended.Text != "B" {
		t.Errorf("sent text = %q, want %q", appended.Text, "B")
	}
	if !b.Fullscreen() {
		t.Errorf("Bridge.Fullscreen() = false, want true after SetFullscreen(true)")
	}
}
