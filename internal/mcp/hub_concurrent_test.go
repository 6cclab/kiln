package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConnectAllConnectsServersConcurrently: servers connect at the same
// time, so two that never answer cost one timeout, not two, and both are
// reported as pending until their attempts finish. Each fake server records
// that it started and then stays up until the test releases it, so both
// being started at once is the proof of concurrency; nothing is timed.
func TestConnectAllConnectsServersConcurrently(t *testing.T) {
	t.Setenv(connectTimeoutEnv, "60s")
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	server := func(name string) ServerConfig {
		script := fmt.Sprintf(`touch %[1]s/%[2]s.started; i=0; while [ ! -f %[3]s ] && [ $i -lt 3000 ]; do sleep 0.02; i=$((i+1)); done`, dir, name, release)
		return ServerConfig{Command: "/bin/sh", Args: []string{"-c", script}}
	}
	h := NewHub()
	defer h.Close(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ConnectAll(context.Background(), map[string]ServerConfig{"a": server("a"), "b": server("b")})
	}()
	releaseServers := func() {
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Errorf("release: %v", err)
		}
	}

	bothStarted := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		_, errA := os.Stat(filepath.Join(dir, "a.started"))
		_, errB := os.Stat(filepath.Join(dir, "b.started"))
		if errA == nil && errB == nil {
			bothStarted = true
			break
		}
	}
	if !bothStarted {
		releaseServers()
		<-done
		t.Fatal("the second server was not started while the first was still connecting; the servers connected one after another")
	}
	if n := len(h.Pending()); n != 2 {
		t.Errorf("Pending() mid-connect = %d servers, want 2", n)
	}
	releaseServers()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("ConnectAll did not return after the servers exited")
	}
	if n := len(h.Pending()); n != 0 {
		t.Errorf("Pending() after ConnectAll = %d servers, want 0", n)
	}
	if n := len(h.Statuses()); n != 2 {
		t.Errorf("Statuses() = %d, want 2", n)
	}
}
