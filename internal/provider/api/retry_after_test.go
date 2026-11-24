package api

import (
	"net/http"
	"testing"
	"time"
)

// TestNewStatusErrorReadsRetryAfter: both Retry-After forms (RFC 9110
// §10.2.3, delay-seconds and an HTTP date) become StatusError.RetryAfter;
// a missing, negative, unparseable or past value is zero.
func TestNewStatusErrorReadsRetryAfter(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		header string
		want   time.Duration
	}{
		{"30", 30 * time.Second},
		{" 1.5 ", 1500 * time.Millisecond},
		{"", 0},
		{"-5", 0},
		{"soon", 0},
		{now.Add(-time.Minute).UTC().Format(http.TimeFormat), 0},
	} {
		if got := parseRetryAfter(c.header, now); got != c.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", c.header, got, c.want)
		}
	}
	date := now.Add(45 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(date, now); got < 43*time.Second || got > 45*time.Second {
		t.Errorf("parseRetryAfter(HTTP date 45s ahead) = %v, want about 45s", got)
	}

	resp := &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"12"}}}
	err := NewStatusError(resp, []byte(`{"error":{"message":"slow down"}}`))
	if err.Status != 429 || !err.Retriable || err.RetryAfter != 12*time.Second || err.RetryAfterDelay() != 12*time.Second {
		t.Errorf("NewStatusError = %+v, want 429, retriable, RetryAfter 12s", err)
	}
}
