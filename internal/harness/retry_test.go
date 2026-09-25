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

func TestIsRetriable_PlainUnexpectedEOFIsNotEnough(t *testing.T) {
	// A bare io.ErrUnexpectedEOF, not wrapped in StreamInterrupted, isn't
	// recognized by isRetriable's existing net.Error / string-sniffing
	// checks -- this documents why the provider layer must wrap it rather
	// than relying on isRetriable's fallback to catch it.
	if isRetriable(io.ErrUnexpectedEOF) {
		t.Fatalf("isRetriable(io.ErrUnexpectedEOF) = true; if this changed, StreamInterrupted's wrapping may be redundant, but note where the recognition moved to")
	}
}
