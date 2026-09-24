package harness

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"strings"
	"time"
)

// RetryPolicy is pi's NormalizedRetryPolicy: retry on retriable provider
// errors (429/529/5xx/network) with exponential backoff capped at
// MaxAgentDelayMs, up to MaxAttempts total attempts. Defaults match the
// values recorded in the reference session's generationContext.retryPolicy.
type RetryPolicy struct {
	BaseDelayMs     int `json:"baseDelayMs"`
	MaxAgentDelayMs int `json:"maxAgentDelayMs"`
	MaxAttempts     int `json:"maxAttempts"`
}

// DefaultRetryPolicy matches the reference session's recorded defaults.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{BaseDelayMs: 1000, MaxAgentDelayMs: 60000, MaxAttempts: 4}
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.BaseDelayMs <= 0 {
		p.BaseDelayMs = 1000
	}
	if p.MaxAgentDelayMs <= 0 {
		p.MaxAgentDelayMs = 60000
	}
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 4
	}
	return p
}

// delay computes the backoff for the given 1-based attempt, with full
// jitter, capped at MaxAgentDelayMs.
func (p RetryPolicy) delay(attempt int) time.Duration {
	p = p.normalized()
	ms := p.BaseDelayMs * (1 << uint(attempt-1))
	if ms > p.MaxAgentDelayMs {
		ms = p.MaxAgentDelayMs
	}
	jittered := rand.Intn(ms + 1)
	return time.Duration(jittered) * time.Millisecond
}

// RetriableError is implemented by provider errors that carry an HTTP
// status, so isRetriable can recognize 429/5xx without string sniffing.
type RetriableError interface {
	error
	StatusCode() int
}

// isRetriable mirrors pi's retry predicate: HTTP 429/529/5xx, or a network
// error (connection reset, timeout, DNS failure).
func isRetriable(err error) bool {
	if err == nil {
		return false
	}
	var re RetriableError
	if errors.As(err, &re) {
		code := re.StatusCode()
		return code == 429 || code == 529 || (code >= 500 && code < 600)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"connection reset", "econnreset", "429", "529", "overloaded", "timeout"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
