package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// connectAllWithin runs ConnectAll and fails the test if it has not
// returned within limit, so a regression that wedges it cannot hang the
// suite. It returns how long ConnectAll took.
func connectAllWithin(t *testing.T, h *Hub, configs map[string]ServerConfig, limit time.Duration) time.Duration {
	t.Helper()
	started := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ConnectAll(context.Background(), configs)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("ConnectAll did not return within %s", limit)
	}
	return time.Since(started)
}

// TestConnectAllCapsLocalServers: Claude Code connects at most three stdio
// servers at a time. Four servers that never answer, each with a 1s
// timeout, therefore take two rounds: three, then the fourth once a slot
// frees. Uncapped, all four time out together in about one second.
func TestConnectAllCapsLocalServers(t *testing.T) {
	t.Setenv(connectTimeoutEnv, "1s")
	t.Setenv(localConnectConcurrencyEnv, "")
	silent := ServerConfig{Command: "/bin/sh", Args: []string{"-c", "sleep 30"}}
	h := NewHub()
	defer h.Close(context.Background())

	configs := map[string]ServerConfig{}
	for i := 0; i < localConnectConcurrency+1; i++ {
		configs[fmt.Sprintf("a%d", i)] = silent
	}
	elapsed := connectAllWithin(t, h, configs, 20*time.Second)
	if elapsed < 1900*time.Millisecond {
		t.Errorf("ConnectAll took %s for %d silent stdio servers; more than %d connected at once", elapsed, len(configs), localConnectConcurrency)
	}
	if n := len(h.Statuses()); n != len(configs) {
		t.Errorf("Statuses() = %d, want %d", n, len(configs))
	}
}

// peakServer is an HTTP server that refuses every request and records the
// most requests it ever had in flight at once. With need > 1 it holds each
// request until that many are in flight together (or the client gives up),
// so "this many connected at once" is observed rather than timed.
type peakServer struct {
	*httptest.Server
	mu      sync.Mutex
	inFlite int
	peak    int
	reached chan struct{}
}

func newPeakServer(need int) *peakServer {
	p := &peakServer{reached: make(chan struct{})}
	var once sync.Once
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.inFlite++
		p.peak = max(p.peak, p.inFlite)
		if p.inFlite >= need {
			once.Do(func() { close(p.reached) })
		}
		p.mu.Unlock()
		// Leave before the response is written: the client may send its next
		// request the instant it has the answer, and that one is not
		// concurrent with this.
		leave := func() {
			p.mu.Lock()
			p.inFlite--
			p.mu.Unlock()
		}
		if need > 1 {
			select {
			case <-p.reached:
			case <-r.Context().Done():
				leave()
				return
			case <-time.After(30 * time.Second):
			}
		} else {
			// Long enough for an uncapped second connection to overlap.
			time.Sleep(50 * time.Millisecond)
		}
		leave()
		http.Error(w, "no", http.StatusNotFound)
	}))
	return p
}

func (p *peakServer) Peak() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

// TestConnectAllRemoteServersHaveTheirOwnCap: network servers are capped
// by MCP_REMOTE_SERVER_CONNECTION_BATCH_SIZE, not by the stdio cap. The
// fixture records its peak concurrent requests: two servers reach two at
// once when only the stdio cap is 1, and never more than one when the
// remote cap is 1.
func TestConnectAllRemoteServersHaveTheirOwnCap(t *testing.T) {
	run := func(localCap, remoteCap string, srv *peakServer) {
		t.Setenv(connectTimeoutEnv, "60s")
		t.Setenv(localConnectConcurrencyEnv, localCap)
		t.Setenv(remoteConnectConcurrencyEnv, remoteCap)
		configs := map[string]ServerConfig{
			"r1": {Type: "http", URL: srv.URL},
			"r2": {Type: "http", URL: srv.URL},
		}
		h := NewHub()
		defer h.Close(context.Background())
		connectAllWithin(t, h, configs, 90*time.Second)
		if n := len(h.Statuses()); n != len(configs) {
			t.Errorf("Statuses() = %d, want %d", n, len(configs))
		}
	}

	// Stdio cap 1 must not throttle network servers: both reach the fixture
	// together (each request waits for the other, so this cannot be missed
	// on a slow machine; if they shared the stdio cap it would take 30s and
	// fail).
	both := newPeakServer(2)
	defer both.Close()
	run("1", "", both)
	if got := both.Peak(); got < 2 {
		t.Errorf("stdio cap 1: peak concurrent network requests = %d, want 2: they shared the stdio cap", got)
	}

	// Remote cap 1 serialises them: never two requests at once.
	capped := newPeakServer(1)
	defer capped.Close()
	run("", "1", capped)
	if got := capped.Peak(); got != 1 {
		t.Errorf("remote cap 1: peak concurrent network requests = %d, want 1", got)
	}
}

func TestConnectConcurrencyEnv(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want int
	}{
		{"", 3},
		{"5", 5},
		{" 7 ", 7},
		{"0", 3},
		{"-2", 3},
		{"many", 3},
	} {
		t.Setenv(localConnectConcurrencyEnv, tc.val)
		if got := connectConcurrency(localConnectConcurrencyEnv, 3); got != tc.want {
			t.Errorf("%s=%q: got %d, want %d", localConnectConcurrencyEnv, tc.val, got, tc.want)
		}
	}
}
