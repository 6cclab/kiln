package mcp

import (
	"context"
	"testing"
	"time"
)

// TestConnectAllConnectsServersConcurrently: servers connect at the same
// time, so two that never answer cost one timeout, not two, and both are
// reported as pending until their attempts finish.
func TestConnectAllConnectsServersConcurrently(t *testing.T) {
	t.Setenv(connectTimeoutEnv, "1s")
	silent := ServerConfig{Command: "/bin/sh", Args: []string{"-c", "sleep 30"}}
	h := NewHub()
	defer h.Close(context.Background())

	pendingSeen := make(chan int, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		pendingSeen <- len(h.Pending())
	}()
	started := time.Now()
	h.ConnectAll(context.Background(), map[string]ServerConfig{"a": silent, "b": silent})
	elapsed := time.Since(started)

	if elapsed > 1900*time.Millisecond {
		t.Errorf("ConnectAll took %s for two 1s timeouts; the servers connected one after another", elapsed)
	}
	if n := <-pendingSeen; n != 2 {
		t.Errorf("Pending() mid-connect = %d servers, want 2", n)
	}
	if n := len(h.Pending()); n != 0 {
		t.Errorf("Pending() after ConnectAll = %d servers, want 0", n)
	}
	if n := len(h.Statuses()); n != 2 {
		t.Errorf("Statuses() = %d, want 2", n)
	}
}
