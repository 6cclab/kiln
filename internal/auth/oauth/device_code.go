package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// device_code.go is the shared RFC 8628 device-authorization polling loop
// every device-code flow (GitHub Copilot, Kimi, xAI, OpenAI Codex's
// device_code login method, Radius's device-code login method) builds on,
// ported from pi-ai's dist/auth/oauth/device-code.js's
// pollOAuthDeviceCodeFlow.

// DeviceStatus is one poll's outcome, matching pi's
// "pending" | "complete" | "failed" | "slow_down" union.
type DeviceStatus int

const (
	DevicePending DeviceStatus = iota
	DeviceComplete
	DeviceFailed
	DeviceSlowDown
)

// DevicePollResult is what one poll attempt reports.
type DevicePollResult[T any] struct {
	Status DeviceStatus
	// Value is populated when Status == DeviceComplete.
	Value T
	// Message is populated when Status == DeviceFailed.
	Message string
	// IntervalSeconds, when Status == DeviceSlowDown and the server reported
	// a new required minimum interval (e.g. GitHub Copilot's `interval`
	// field), overrides the client-tracked backoff. nil means "the server
	// didn't say; apply the default +5s backoff."
	IntervalSeconds *float64
}

// DevicePollOptions configures PollDeviceCode, mirroring pi's
// PollOAuthDeviceCodeFlowOptions.
type DevicePollOptions struct {
	// IntervalSeconds is the server-advertised poll interval. Zero means "not
	// specified"; the RFC 8628 3.2 default of 5 seconds is used.
	IntervalSeconds float64
	// ExpiresInSeconds bounds the whole poll; zero means "no deadline"
	// (matches pi's Number.POSITIVE_INFINITY case, though every flow in this
	// package always sets one).
	ExpiresInSeconds float64
	// WaitBeforeFirstPoll sleeps one interval before the first poll instead
	// of polling immediately (GitHub Copilot and Kimi set this since the
	// user hasn't had time to enter the code yet).
	WaitBeforeFirstPoll bool
}

// minimumIntervalMs and friends mirror device-code.js's constants exactly.
const (
	minimumIntervalMs             = 1000
	defaultPollIntervalSecs       = 5
	slowDownIncrementMs     int64 = 5000
)

var (
	// ErrDeviceCancelled matches pi's CANCEL_MESSAGE ("Login cancelled").
	ErrDeviceCancelled = errors.New("login cancelled")
	// ErrDeviceTimeout matches pi's TIMEOUT_MESSAGE.
	ErrDeviceTimeout = errors.New("device flow timed out")
	// ErrDeviceSlowDownTimeout matches pi's SLOW_DOWN_TIMEOUT_MESSAGE.
	ErrDeviceSlowDownTimeout = errors.New("device flow timed out after one or more slow_down responses. This is often caused by clock drift in WSL or VM environments. Please sync or restart the VM clock and try again")
)

// abortableSleep sleeps for d, or returns ErrDeviceCancelled early if ctx is
// cancelled first, matching device-code.js's abortableSleep.
func abortableSleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ErrDeviceCancelled
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ErrDeviceCancelled
	}
}

// PollDeviceCode runs poll on a timer until it reports DeviceComplete,
// DeviceFailed, ctx is cancelled, or the deadline (ExpiresInSeconds) passes,
// matching device-code.js's pollOAuthDeviceCodeFlow exactly: minimum interval
// 1s, default interval 5s absent a server value, slow_down either adopts the
// server's reported interval or backs off by 5s (RFC 8628 3.5), and a
// timeout after one or more slow_down responses gets the WSL/VM clock-drift
// hint pi's error message carries.
func PollDeviceCode[T any](ctx context.Context, opts DevicePollOptions, poll func(context.Context) DevicePollResult[T]) (T, error) {
	var zero T

	var deadline time.Time
	hasDeadline := opts.ExpiresInSeconds > 0
	if hasDeadline {
		deadline = time.Now().Add(time.Duration(opts.ExpiresInSeconds * float64(time.Second)))
	}

	intervalSecs := opts.IntervalSeconds
	if intervalSecs <= 0 {
		intervalSecs = defaultPollIntervalSecs
	}
	intervalMs := int64(intervalSecs * 1000)
	if intervalMs < minimumIntervalMs {
		intervalMs = minimumIntervalMs
	}

	slowDownResponses := 0

	sleepCapped := func(ms int64) error {
		d := time.Duration(ms) * time.Millisecond
		if hasDeadline {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return nil
			}
			if remaining < d {
				d = remaining
			}
		}
		return abortableSleep(ctx, d)
	}

	if opts.WaitBeforeFirstPoll {
		if hasDeadline && !time.Now().Before(deadline) {
			// no time remaining; fall through to the deadline check below
		} else if err := sleepCapped(intervalMs); err != nil {
			return zero, err
		}
	}

	for !hasDeadline || time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return zero, ErrDeviceCancelled
		}
		result := poll(ctx)
		switch result.Status {
		case DeviceComplete:
			return result.Value, nil
		case DeviceFailed:
			return zero, fmt.Errorf("%s", result.Message)
		case DeviceSlowDown:
			slowDownResponses++
			if result.IntervalSeconds != nil && *result.IntervalSeconds > 0 {
				ms := int64(*result.IntervalSeconds * 1000)
				if ms < minimumIntervalMs {
					ms = minimumIntervalMs
				}
				intervalMs = ms
			} else {
				intervalMs += slowDownIncrementMs
				if intervalMs < minimumIntervalMs {
					intervalMs = minimumIntervalMs
				}
			}
		}

		if hasDeadline && !time.Now().Before(deadline) {
			break
		}
		if err := sleepCapped(intervalMs); err != nil {
			return zero, err
		}
	}

	if slowDownResponses > 0 {
		return zero, ErrDeviceSlowDownTimeout
	}
	return zero, ErrDeviceTimeout
}

// SelectOption is one choice in a FlowInteraction.PromptSelect prompt,
// mirroring auth.SelectOption without importing internal/auth (oauth must
// stay importable from internal/auth without a cycle).
type SelectOption struct {
	ID    string
	Label string
}

// FlowInteraction extends the narrower Interaction (anthropic.go) with the
// two prompt/notify kinds the device-code and multi-method flows in this
// package need: a device-code notification (user code + verification URI)
// and a method-select prompt (OpenAI Codex's browser-vs-device-code choice,
// Radius's browser-vs-device-code choice).
type FlowInteraction interface {
	Interaction
	// NotifyDeviceCode is called once with the code to show and the URL to
	// open.
	NotifyDeviceCode(userCode, verificationURI string, intervalSeconds, expiresInSeconds int)
	// PromptSelect blocks for a choice among options, returning the chosen
	// option's ID.
	PromptSelect(ctx context.Context, message string, options []SelectOption) (string, error)
}

// Token is the canonical (refresh, access, expires-at-epoch-ms) shape every
// flow in this package produces, matching auth.OAuthCredential's fields
// one-for-one so login.go can store it verbatim.
type Token struct {
	Refresh string
	Access  string
	Expires int64
}

// httpClientTimeout bounds every request the flows in this package make,
// matching pi's per-flow AbortSignal.timeout guards (30s for token
// exchanges, 5s for Copilot's rate-limited calls -- 30s is used uniformly
// here since none of these flows is latency-sensitive).
const httpClientTimeout = 30 * time.Second

// doRequest issues req (already built) with a bounded timeout layered onto
// ctx, and returns the raw response body alongside status/error handling
// callers can specialize. The caller must not also set a body-read timeout.
func doRequest(ctx context.Context, req *http.Request) (status int, body []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, httpClientTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ErrDeviceCancelled
		}
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

// postForm POSTs an application/x-www-form-urlencoded body and decodes the
// JSON response into a map, matching every device-code/token endpoint in
// this package.
func postForm(ctx context.Context, rawURL string, fields url.Values, extraHeaders map[string]string) (status int, body map[string]any, raw []byte, err error) {
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(fields.Encode()))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	code, b, err := doRequest(ctx, req)
	if err != nil {
		return 0, nil, nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m) // best-effort; callers check for expected fields
	return code, m, b, nil
}

// postJSON POSTs a JSON body and decodes the JSON response into a map.
func postJSON(ctx context.Context, rawURL string, payload any, extraHeaders map[string]string) (status int, body map[string]any, raw []byte, err error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(string(encoded)))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	code, b, err := doRequest(ctx, req)
	if err != nil {
		return 0, nil, nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return code, m, b, nil
}

// getJSON GETs and decodes a JSON response into a map, with optional bearer
// auth (GitHub Copilot's copilot_internal/v2/token exchange).
func getJSON(ctx context.Context, rawURL string, extraHeaders map[string]string) (status int, body map[string]any, raw []byte, err error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	code, b, err := doRequest(ctx, req)
	if err != nil {
		return 0, nil, nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return code, m, b, nil
}

// str reads a required string field from a decoded JSON map body.
func str(m map[string]any, key string) (string, bool) {
	v, ok := m[key].(string)
	return v, ok && v != ""
}

// num reads a numeric field (JSON numbers decode to float64) from a decoded
// JSON map body.
func num(m map[string]any, key string) (float64, bool) {
	v, ok := m[key].(float64)
	return v, ok
}
