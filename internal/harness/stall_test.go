package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A model that sends nothing at all is ended by the first-token allowance,
// not left to hang until a whole-request client timeout (or forever).
func TestRequest_StallBeforeFirstTokenEndsRequest(t *testing.T) {
	r := newTestRig(t, `
steps:
  - delay: 3s
    text: "too late"
`, nil)
	r.H.opts.Retry = RetryPolicy{BaseDelayMs: 1, MaxAgentDelayMs: 1, MaxAttempts: 1}
	r.H.opts.StallFirstEvent = func(int) time.Duration { return 300 * time.Millisecond }
	r.H.opts.StallIdle = time.Second

	start := time.Now()
	res, err := r.mustLane("main").Prompt(context.Background(), "hello", nil)
	var stalled *StallError
	if res.Status != StatusFailed || !errors.As(err, &stalled) || !stalled.FirstToken {
		t.Fatalf("status %s err %v, want failed with a first-token StallError", res.Status, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("stall took %s to end the request, want ~300ms", took)
	}
}

// A slow but progressing stream is not cut off however long it runs in
// total: only a gap longer than the idle limit ends it. The stream here
// runs well past the first-token allowance and the idle limit combined.
func TestRequest_SlowProgressingStreamCompletes(t *testing.T) {
	r := newTestRig(t, `
steps:
  - delay: 200ms
    chunk_delay: 120ms
    text: "`+strings.Repeat("slow tokens arrive steadily ", 4)+`"
`, nil)
	r.H.opts.Retry = RetryPolicy{BaseDelayMs: 1, MaxAgentDelayMs: 1, MaxAttempts: 1}
	r.H.opts.StallFirstEvent = func(int) time.Duration { return 400 * time.Millisecond }
	r.H.opts.StallIdle = 400 * time.Millisecond

	start := time.Now()
	res, err := r.mustLane("main").Prompt(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res.Status != StatusCompleted {
		t.Fatalf("status %s err %v, want completed", res.Status, res.Error)
	}
	if took := time.Since(start); took < 800*time.Millisecond {
		t.Fatalf("stream took only %s; the test needs it to outlast first+idle (800ms) to prove there is no total deadline", took)
	}
}

// A stall is retried like a dropped connection.
func TestIsRetriable_Stall(t *testing.T) {
	if !isRetriable(&StallError{Waited: time.Second}) {
		t.Fatal("a stall must be retriable like a dropped connection")
	}
}
