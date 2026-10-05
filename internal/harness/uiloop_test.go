package harness

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
)

// A UI calls Lane.Abort, Lane.Steer and Lane.ClearInbox from its own event
// loop while a turn runs, and the UI's event handlers hand messages to
// that same loop and wait for it to take them (Bubble Tea's Program.Send,
// an unbuffered channel). These calls used to emit their events
// synchronously, which waits for the bus: while the lane's goroutine was
// inside a handler blocked on the loop, the loop blocked on the bus, and
// the TUI froze mid-stream with its busy row stuck (1 run in 12 to 15 of
// TestTUI_StopHook_SkippedWhenTheTurnIsInterrupted at GOMAXPROCS=1).
//
// The test stands in for that loop with an unbuffered channel. A handler
// sends into it on the first streamed token; the loop, instead of reading,
// makes the lane call while that send is pending. The call must return.
func TestLaneCallsFromTheUILoopDoNotWaitForTheBus(t *testing.T) {
	type lanecall struct {
		pre  func(l *Lane) error // run before the handler starts blocking
		call func(l *Lane) error
	}
	calls := map[string]lanecall{
		"Abort": {call: func(l *Lane) error { return l.Abort() }},
		"Steer": {call: func(l *Lane) error { return l.SteerAs("and also this", "and also this") }},
		"ClearInbox": {
			// Something to withdraw: ClearInbox on an empty inbox emits nothing.
			pre: func(l *Lane) error { return l.SteerAs("queued", "queued") },
			call: func(l *Lane) error {
				_, err := l.ClearInbox()
				return err
			},
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			rig := newTestRig(t, `
model: faux-1
steps:
  - chunk_delay: 20ms
    text: "`+strings.Repeat("Slow words keep streaming in. ", 10)+`"
    end_turn: true
  - text: "and done"
    end_turn: true
`, []string{"bash"})
			lane := rig.mustLane("main")

			loop := make(chan string) // the UI's event loop: unbuffered
			streaming, inHandler := make(chan struct{}), make(chan struct{})
			var streamingOnce, inHandlerOnce sync.Once
			var armed atomic.Bool
			rig.H.Events().OnAll(func(ev Event) {
				if ev.Type != EventMessageUpdate || ev.StreamEvent == nil || ev.StreamEvent.Type != msg.EventTextDelta {
					return
				}
				streamingOnce.Do(func() { close(streaming) })
				if !armed.Load() {
					return
				}
				inHandlerOnce.Do(func() { close(inHandler) })
				loop <- ev.StreamEvent.Delta
			})

			done := make(chan RunResult, 1)
			go func() {
				res, _ := lane.Prompt(context.Background(), "write slowly", nil)
				done <- res
			}()

			<-streaming
			if call.pre != nil {
				if err := call.pre(lane); err != nil {
					t.Fatalf("%s setup: %v", name, err)
				}
			}
			armed.Store(true)
			<-inHandler // the lane is in a handler, waiting for the loop
			returned := make(chan error, 1)
			go func() { returned <- call.call(lane) }()

			select {
			case err := <-returned:
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			case <-time.After(3 * time.Second):
				// Unwedge the lane so the test can finish, then fail.
				go func() {
					for range loop {
					}
				}()
				<-returned
				t.Fatalf("%s blocked on the event bus while a handler waited for the UI loop", name)
			}

			// The loop resumes reading; the turn must finish.
			go func() {
				for range loop {
				}
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the turn never finished")
			}
			close(loop)
		})
	}
}

// Post delivers events in the order it was called, without waiting for
// their handlers.
func TestEventsPostIsOrderedAndDoesNotWait(t *testing.T) {
	e := NewEvents()
	release := make(chan struct{})
	var mu sync.Mutex
	var got []int
	e.On(EventQueueUpdate, func(ev Event) {
		<-release
		mu.Lock()
		got = append(got, ev.QueueLen)
		mu.Unlock()
	})
	for i := 1; i <= 5; i++ {
		n := i
		e.Post(func() Event { return Event{Type: EventQueueUpdate, QueueLen: n * 10} })
	}
	// Every Post returned while the first handler is still blocked.
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivered %d of 5 posted events", n)
		}
		time.Sleep(time.Millisecond)
	}
	for i, v := range got {
		if v != (i+1)*10 {
			t.Fatalf("delivered %v, want 10,20,30,40,50 in order", got)
		}
	}
}
