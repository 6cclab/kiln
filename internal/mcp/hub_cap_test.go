package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// TestConnectAllRemoteServersHaveTheirOwnCap: network servers are capped
// by MCP_REMOTE_SERVER_CONNECTION_BATCH_SIZE, not by the stdio cap. Every
// request to the fixture is refused after a delay, so one server's failed
// attempt takes a fixed time (measured first, since the SDK makes more
// than one request). Two servers then take about one attempt when only
// the stdio cap is 1, and about two when the remote cap is 1.
func TestConnectAllRemoteServersHaveTheirOwnCap(t *testing.T) {
	const delay = 300 * time.Millisecond
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
		http.Error(w, "no", http.StatusNotFound)
	}))
	defer slow.Close()
	run := func(localCap, remoteCap string, configs map[string]ServerConfig) time.Duration {
		t.Setenv(connectTimeoutEnv, "10s")
		t.Setenv(localConnectConcurrencyEnv, localCap)
		t.Setenv(remoteConnectConcurrencyEnv, remoteCap)
		h := NewHub()
		defer h.Close(context.Background())
		elapsed := connectAllWithin(t, h, configs, 30*time.Second)
		if n := len(h.Statuses()); n != len(configs) {
			t.Errorf("Statuses() = %d, want %d", n, len(configs))
		}
		return elapsed
	}
	one := run("", "", map[string]ServerConfig{"r1": {Type: "http", URL: slow.URL}})
	if one < delay {
		t.Fatalf("one network server took %s, under the fixture's %s delay", one, delay)
	}
	two := map[string]ServerConfig{
		"r1": {Type: "http", URL: slow.URL},
		"r2": {Type: "http", URL: slow.URL},
	}
	if elapsed := run("1", "", two); elapsed > one*3/2 {
		t.Errorf("stdio cap 1: two network servers took %s (one alone: %s), so they shared the stdio cap", elapsed, one)
	}
	if elapsed := run("", "1", two); elapsed < one*9/5 {
		t.Errorf("remote cap 1: two network servers took %s (one alone: %s), so both connected at once", elapsed, one)
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
