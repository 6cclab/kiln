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
// total: only a gap longer than the idle limit ends it. The stream here
// runs longer than the first-token allowance and the idle limit combined.
// Each gap (100ms) is a twentieth of the idle limit, so only a machine
// stalled for two seconds mid-stream could cut it off.
func TestRequest_SlowProgressingStreamCompletes(t *testing.T) {
	const limit = 2 * time.Second
	// 8-character chunks, 100ms apart: 46 chunks take over 4.5s.
	r := newTestRig(t, `
steps:
  - delay: 100ms
    chunk_delay: 100ms
    text: "`+strings.Repeat("slow tokens arrive steadily ", 13)+`"
`, nil)
	r.H.opts.Retry = RetryPolicy{BaseDelayMs: 1, MaxAgentDelayMs: 1, MaxAttempts: 1}
	r.H.opts.StallFirstEvent = func(int) time.Duration { return limit }
	r.H.opts.StallIdle = limit

	start := time.Now()
	res, err := r.mustLane("main").Prompt(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res.Status != StatusCompleted {
		t.Fatalf("status %s err %v, want completed", res.Status, res.Error)
	}
	if took := time.Since(start); took < 2*limit {
		t.Fatalf("stream took only %s; the test needs it to outlast first+idle (%s) to prove there is no total deadline", took, 2*limit)
	}
}

// A stall is retried like a dropped connection.
func TestIsRetriable_Stall(t *testing.T) {
	if !isRetriable(&StallError{Waited: time.Second}) {
		t.Fatal("a stall must be retriable like a dropped connection")
	}
}
