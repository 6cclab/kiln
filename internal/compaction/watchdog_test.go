package compaction

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// manualTimers replaces afterFunc with timers that never fire on their own
// and whose Stop always reports the callback as already started — the
// worst case time.Timer.Stop allows. The test fires callbacks by hand.
type manualTimers struct {
	mu  sync.Mutex
	fns []func()
}

type lateStop struct{}

func (lateStop) Stop() bool { return false }

func useManualTimers(t *testing.T) *manualTimers {
	t.Helper()
	m := &manualTimers{}
	prev := afterFunc
	afterFunc = func(_ time.Duration, f func()) stopper {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.fns = append(m.fns, f)
		return lateStop{}
	}
	t.Cleanup(func() { afterFunc = prev })
	return m
}

func (m *manualTimers) armed() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.fns)
}

func (m *manualTimers) fire(i int) {
	m.mu.Lock()
	f := m.fns[i]
	m.mu.Unlock()
	f()
}

// TestWatchdogStaleCallbackDoesNotFire: a callback whose limit expired as
// a Reset came in (Stop could not prevent it) must not fire; only the
// latest arming can, and only once.
func TestWatchdogStaleCallbackDoesNotFire(t *testing.T) {
	m := useManualTimers(t)
	fired := 0
	w := NewWatchdog(time.Second, func() { fired++ })
	w.Reset(time.Second, func() { fired++ })
	m.fire(0)
	if fired != 0 {
		t.Fatalf("superseded callback fired")
	}
	m.fire(1)
	m.fire(1)
	if fired != 1 {
		t.Fatalf("current callback fired %d times, want 1", fired)
	}
	w.Reset(time.Second, func() { fired++ })
	if n := m.armed(); n != 2 {
		t.Fatalf("Reset after firing re-armed the watchdog (%d timers)", n)
	}

	w2 := NewWatchdog(time.Second, func() { fired++ })
	w2.Stop()
	m.fire(2)
	if fired != 1 {
		t.Fatalf("callback fired after Stop")
	}
}

// resumingStreamer streams three tokens. After the second has been taken
// (so the first token's Reset has run), it fires every limit armed so far
// — callbacks that had already started when the tokens re-armed the
// watchdog — then reports whether the request was cancelled.
type resumingStreamer struct {
	timers *manualTimers
}

func (s resumingStreamer) Stream(ctx context.Context, _ provider.Model, _ []msg.Message, _ provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	ch := make(chan msg.StreamEvent)
	go func() {
		defer close(ch)
		ch <- msg.StreamEvent{Type: msg.EventTextDelta, Delta: "a"}
		ch <- msg.StreamEvent{Type: msg.EventTextDelta, Delta: "b"}
		// The unbuffered send of "c" returns only once the consumer has
		// finished with "b" and come back for more.
		ch <- msg.StreamEvent{Type: msg.EventTextDelta, Delta: "c"}
		for i := 0; i < s.timers.armed()-1; i++ {
			s.timers.fire(i)
		}
	}()
	return ch, func() (*msg.AssistantMessage, error) {
		if err := ctx.Err(); err != nil {
			return &msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopAborted, ErrorMessage: "Request was aborted"}, nil
		}
		return scriptedAssistant("summary", msg.Usage{Input: 10, Output: 5}), nil
	}
}

// TestRunSimpleWatchedIgnoresSupersededStallTimer: a stall limit that
// expires at the moment a token arrives must not cancel the request the
// token just resumed. Before, the token stopped the old timer (which does
// not stop a callback already running) and armed a new one, and the old
// callback then cancelled the request with "the model sent nothing".
func TestRunSimpleWatchedIgnoresSupersededStallTimer(t *testing.T) {
	m := useManualTimers(t)
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, _, err := runSimpleWatched(context.Background(), resumingStreamer{timers: m}, provider.Model{ID: "m"}, "sys", "prompt", 100, "", Options{IdleTimeout: time.Second}, 1, 1)
		done <- result{text, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("runSimpleWatched: %v", r.err)
		}
		if r.text != "summary" {
			t.Fatalf("text = %q, want summary", r.text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runSimpleWatched did not return")
	}
	if n := m.armed(); n < 3 {
		t.Fatalf("only %d stall timers armed; the tokens did not re-arm the watchdog", n)
	}
}
