package harness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// A model that sends nothing at all is ended by the first-token allowance,
// not left to hang until a whole-request client timeout (or forever).
func TestRequest_StallBeforeFirstTokenEndsRequest(t *testing.T) {
	r := newTestRig(t, `
steps:
  - delay: 60s
    text: "too late"
`, nil)
	r.H.opts.Retry = RetryPolicy{BaseDelayMs: 1, MaxAgentDelayMs: 1, MaxAttempts: 1}
	r.H.opts.StallFirstEvent = func(int) time.Duration { return 300 * time.Millisecond }
	r.H.opts.StallIdle = time.Second

	// The reply is held for 60s, so only the 300ms allowance can end the
	// request: a StallError is the whole proof, with no elapsed-time bound.
	res, err := r.mustLane("main").Prompt(context.Background(), "hello", nil)
	var stalled *StallError
	if res.Status != StatusFailed || !errors.As(err, &stalled) || !stalled.FirstToken {
		t.Fatalf("status %s err %v, want failed with a first-token StallError", res.Status, err)
	}
}

// A slow but progressing stream is not cut off however long it runs in
// total: only a gap longer than the idle limit ends it. The limits run on
// timers that never fire on their own. Each token re-arms the watchdog, and
// the test then fires every limit it superseded, as if the stream had run
// past all of them, so the stream outlasts every limit but the latest.
func TestRequest_SlowProgressingStreamCompletes(t *testing.T) {
	const first, idle = time.Minute, time.Second
	text := strings.Repeat("slow tokens arrive steadily ", 13)
	r := newTestRig(t, `
steps:
  - text: "`+text+`"
`, nil)
	r.H.opts.Retry = RetryPolicy{BaseDelayMs: 1, MaxAgentDelayMs: 1, MaxAttempts: 1}
	r.H.opts.StallFirstEvent = func(int) time.Duration { return first }
	r.H.opts.StallIdle = idle

	var (
		mu      sync.Mutex
		limits  []time.Duration
		pending []func()
		expired sync.WaitGroup
	)
	r.H.opts.StallTimer = func(d time.Duration, f func()) func() bool {
		mu.Lock()
		defer mu.Unlock()
		limits = append(limits, d)
		// Every earlier limit expires now. The watchdog holds its lock while
		// it arms, so each callback runs once the re-arm is done.
		for _, fire := range pending {
			expired.Add(1)
			go func() { defer expired.Done(); fire() }()
		}
		pending = []func(){f}
		return func() bool { return false } // too late to stop, the worst case
	}

	res, err := r.mustLane("main").Prompt(context.Background(), "hello", nil)
	expired.Wait()
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res.Status != StatusCompleted {
		t.Fatalf("status %s err %v, want completed", res.Status, res.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	// faux streams 8-character chunks; each one must restart the idle limit.
	if len(limits) == 0 || limits[0] != first {
		t.Fatalf("limits armed %v, want the first-token allowance %v first", limits, first)
	}
	if chunks := len(text) / 8; len(limits)-1 < chunks {
		t.Fatalf("watchdog re-armed %d times for %d chunks: progress must restart the idle limit", len(limits)-1, chunks)
	}
	for i, d := range limits[1:] {
		if d != idle {
			t.Fatalf("re-arm %d used %v, want the idle limit %v", i+1, d, idle)
		}
	}
}

// A stall is retried like a dropped connection.
func TestIsRetriable_Stall(t *testing.T) {
	if !isRetriable(&StallError{Waited: time.Second}) {
		t.Fatal("a stall must be retriable like a dropped connection")
	}
}
