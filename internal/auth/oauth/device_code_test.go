package oauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPollDeviceCodeCompletesAfterPending(t *testing.T) {
	calls := 0
	got, err := PollDeviceCode(context.Background(), DevicePollOptions{IntervalSeconds: 0.01, ExpiresInSeconds: 5},
		func(ctx context.Context) DevicePollResult[string] {
			calls++
			if calls < 3 {
				return DevicePollResult[string]{Status: DevicePending}
			}
			return DevicePollResult[string]{Status: DeviceComplete, Value: "token-value"}
		})
	if err != nil {
		t.Fatalf("PollDeviceCode: %v", err)
	}
	if got != "token-value" {
		t.Fatalf("got %q, want %q", got, "token-value")
	}
	if calls != 3 {
		t.Fatalf("polled %d times, want 3", calls)
	}
}

func TestPollDeviceCodeFailed(t *testing.T) {
	_, err := PollDeviceCode(context.Background(), DevicePollOptions{IntervalSeconds: 0.01, ExpiresInSeconds: 5},
		func(ctx context.Context) DevicePollResult[string] {
			return DevicePollResult[string]{Status: DeviceFailed, Message: "denied by user"}
		})
	if err == nil || err.Error() != "denied by user" {
		t.Fatalf("err = %v, want %q", err, "denied by user")
	}
}

// TestPollDeviceCodeSlowDownAdoptsServerInterval asserts a slow_down result
// carrying a server-reported interval overrides the client-tracked backoff,
// matching device-code.js's comment: "trusting only a client-tracked value
// risks polling early forever under WSL/VM clock drift."
func TestPollDeviceCodeSlowDownAdoptsServerInterval(t *testing.T) {
	calls := 0
	var gaps []time.Duration
	last := time.Now()
	serverInterval := 0.02

	_, err := PollDeviceCode(context.Background(), DevicePollOptions{IntervalSeconds: 0.01, ExpiresInSeconds: 5},
		func(ctx context.Context) DevicePollResult[string] {
			now := time.Now()
			gaps = append(gaps, now.Sub(last))
			last = now
			calls++
			if calls == 1 {
				return DevicePollResult[string]{Status: DeviceSlowDown, IntervalSeconds: &serverInterval}
			}
			return DevicePollResult[string]{Status: DeviceComplete, Value: "ok"}
		})
	if err != nil {
		t.Fatalf("PollDeviceCode: %v", err)
	}
	if len(gaps) != 2 {
		t.Fatalf("expected 2 poll calls, got %d", len(gaps))
	}
	// The gap before the second poll should reflect the server's 20ms
	// interval, not the original 10ms one.
	if gaps[1] < 15*time.Millisecond {
		t.Fatalf("gap before second poll = %v, want >= server interval (20ms)", gaps[1])
	}
}

// TestPollDeviceCodeSlowDownDefaultBackoff asserts a slow_down with no
// server interval backs off by the RFC 8628 3.5 default of 5s -- checked
// indirectly via the timeout error carrying the WSL/VM clock-drift hint,
// since actually waiting 5s in a unit test would be too slow.
func TestPollDeviceCodeSlowDownDefaultBackoff(t *testing.T) {
	calls := 0
	_, err := PollDeviceCode(context.Background(), DevicePollOptions{IntervalSeconds: 0.01, ExpiresInSeconds: 0.05},
		func(ctx context.Context) DevicePollResult[string] {
			calls++
			return DevicePollResult[string]{Status: DeviceSlowDown}
		})
	if !errors.Is(err, ErrDeviceSlowDownTimeout) {
		t.Fatalf("err = %v, want ErrDeviceSlowDownTimeout", err)
	}
}

func TestPollDeviceCodeTimeoutWithoutSlowDown(t *testing.T) {
	_, err := PollDeviceCode(context.Background(), DevicePollOptions{IntervalSeconds: 0.01, ExpiresInSeconds: 0.03},
		func(ctx context.Context) DevicePollResult[string] {
			return DevicePollResult[string]{Status: DevicePending}
		})
	if !errors.Is(err, ErrDeviceTimeout) {
		t.Fatalf("err = %v, want ErrDeviceTimeout", err)
	}
}

func TestPollDeviceCodeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := PollDeviceCode(ctx, DevicePollOptions{IntervalSeconds: 0.01, ExpiresInSeconds: 5},
		func(ctx context.Context) DevicePollResult[string] {
			calls++
			return DevicePollResult[string]{Status: DevicePending}
		})
	if !errors.Is(err, ErrDeviceCancelled) {
		t.Fatalf("err = %v, want ErrDeviceCancelled", err)
	}
}

func TestPollDeviceCodeWaitBeforeFirstPoll(t *testing.T) {
	start := time.Now()
	var firstCallAt time.Duration
	_, err := PollDeviceCode(context.Background(), DevicePollOptions{IntervalSeconds: 0.03, ExpiresInSeconds: 5, WaitBeforeFirstPoll: true},
		func(ctx context.Context) DevicePollResult[string] {
			if firstCallAt == 0 {
				firstCallAt = time.Since(start)
			}
			return DevicePollResult[string]{Status: DeviceComplete, Value: "ok"}
		})
	if err != nil {
		t.Fatalf("PollDeviceCode: %v", err)
	}
	if firstCallAt < 20*time.Millisecond {
		t.Fatalf("first poll happened after %v, want >= ~30ms (WaitBeforeFirstPoll)", firstCallAt)
	}
}
