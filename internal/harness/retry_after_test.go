package harness

import (
	"fmt"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/provider/api"
)

// TestRetryPolicy_DelayForHonoursRetryAfter: a provider error that carries
// a Retry-After (api.StatusError, wrapped or not) raises the backoff to
// the server's wait, capped at MaxAgentDelayMs; a shorter Retry-After never
// lowers the backoff; HARNESS_RETRY_DELAY_MS still overrides it.
func TestRetryPolicy_DelayForHonoursRetryAfter(t *testing.T) {
	t.Setenv("HARNESS_RETRY_JITTER", "0")
	p := DefaultRetryPolicy() // 1s base, 60s cap
	limited := func(d time.Duration) error {
		return fmt.Errorf("request: %w", &api.StatusError{Status: 429, Retriable: true, RetryAfter: d})
	}
	for _, c := range []struct {
		name string
		err  error
		want time.Duration
	}{
		{"no Retry-After", &api.StatusError{Status: 503, Retriable: true}, time.Second},
		{"Retry-After 30s", limited(30 * time.Second), 30 * time.Second},
		{"Retry-After past the cap", limited(5 * time.Minute), time.Minute},
		{"Retry-After shorter than the backoff", limited(200 * time.Millisecond), time.Second},
	} {
		if got := p.delayFor(1, c.err); got != c.want {
			t.Errorf("%s: delayFor = %v, want %v", c.name, got, c.want)
		}
	}
	t.Setenv("HARNESS_RETRY_DELAY_MS", "5")
	if got := p.delayFor(1, limited(30*time.Second)); got != 5*time.Millisecond {
		t.Errorf("HARNESS_RETRY_DELAY_MS: delayFor = %v, want 5ms", got)
	}
}
