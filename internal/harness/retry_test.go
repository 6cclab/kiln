package harness

import (
	"fmt"
	"io"
	"testing"

	"github.com/andrepato/harness/internal/provider"
)

// TestIsRetriable_StreamInterrupted covers the fix for a mid-stream
// disconnect not being retried (test/e2e/resilience_test.go's
// TestResilience_StreamCutMidResponse): a provider.StreamInterrupted --
// what internal/provider/api's streaming clients now return for a read
// error (e.g. an unexpected EOF from a cut connection) that happens before
// the stream's terminal event -- must be treated as retriable the same way
// a 429/529/5xx status is, even though its Error() string ("provider:
// stream interrupted: unexpected EOF") doesn't match isRetriable's
// string-sniffing fallback needles.
func TestIsRetriable_StreamInterrupted(t *testing.T) {
	err := provider.StreamInterrupted{Cause: io.ErrUnexpectedEOF}
	if !isRetriable(err) {
		t.Fatalf("isRetriable(%v) = false, want true", err)
	}
	// Wrapped, as it would be if a caller added context, is still
	// recognized via errors.As.
	wrapped := fmt.Errorf("streaming request failed: %w", err)
	if !isRetriable(wrapped) {
		t.Fatalf("isRetriable(wrapped %v) = false, want true", wrapped)
	}
}

// TestRetryPolicy_DelayJitterEnv_Zero covers HARNESS_RETRY_JITTER=0: with
// it set, delay must return exactly the base backoff for a given attempt
// (deterministic), not a random draw in [0, backoff] — the e2e suite
// relies on this to make its countdown/reconnect tests non-flaky (see
// test/e2e/tui_test.go's startTUI).
func TestRetryPolicy_DelayJitterEnv_Zero(t *testing.T) {
	t.Setenv("HARNESS_RETRY_JITTER", "0")
	p := DefaultRetryPolicy()
	for attempt, want := range map[int]int{1: 1000, 2: 2000, 3: 4000} {
		for i := 0; i < 5; i++ {
			got := p.delay(attempt)
			if got.Milliseconds() != int64(want) {
				t.Fatalf("delay(%d) = %v, want exactly %dms with jitter disabled", attempt, got, want)
			}
		}
	}
}

// TestRetryPolicy_DelayJitter_DefaultIsRandom covers the default (no env
// var): delay must still land in [0, backoff] and, over enough draws,
// actually vary -- otherwise a future change could accidentally disable
// jitter unconditionally without this suite noticing.
func TestRetryPolicy_DelayJitter_DefaultIsRandom(t *testing.T) {
	p := DefaultRetryPolicy()
	seen := map[int64]bool{}
	for i := 0; i < 50; i++ {
		got := p.delay(3) // base 4000ms cap
		if got < 0 || got.Milliseconds() > 4000 {
			t.Fatalf("delay(3) = %v, want in [0, 4000ms]", got)
		}
		seen[got.Milliseconds()] = true
	}
	if len(seen) < 2 {
		t.Fatalf("delay(3) returned the same value in every one of 50 draws (%v); jitter looks disabled", seen)
	}
}

func TestIsRetriable_PlainUnexpectedEOFIsNotEnough(t *testing.T) {
	// A bare io.ErrUnexpectedEOF, not wrapped in StreamInterrupted, isn't
	// recognized by isRetriable's existing net.Error / string-sniffing
	// checks -- this documents why the provider layer must wrap it rather
	// than relying on isRetriable's fallback to catch it.
	if isRetriable(io.ErrUnexpectedEOF) {
		t.Fatalf("isRetriable(io.ErrUnexpectedEOF) = true; if this changed, StreamInterrupted's wrapping may be redundant, but note where the recognition moved to")
	}
}
