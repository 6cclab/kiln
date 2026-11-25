package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/testkit/mcpfixture"
)

// eventLog collects Hub.OnEvent calls for a test to wait on.
type eventLog struct {
	mu  sync.Mutex
	evs []string
}

func (l *eventLog) add(server string, ev mcpgate.HubEvent) {
	l.mu.Lock()
	l.evs = append(l.evs, server+":"+string(ev))
	l.mu.Unlock()
}

func (l *eventLog) has(want string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.evs {
		if e == want {
			return true
		}
	}
	return false
}

func (l *eventLog) wait(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !l.has(want) {
		if time.Now().After(deadline) {
			l.mu.Lock()
			defer l.mu.Unlock()
			t.Fatalf("no %s event; got %v", want, l.evs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestToolsListChangedRefreshesTools: like Claude Code, a server that
// announces tools/list_changed has its tools fetched again.
func TestToolsListChangedRefreshesTools(t *testing.T) {
	bin := buildFixture(t)
	gate := filepath.Join(t.TempDir(), "add")
	hub := mcpgate.NewHub()
	log := &eventLog{}
	hub.OnEvent = log.add
	hub.ConnectAll(context.Background(), fixtureConfigs(bin, "--add-tool-gate", gate))
	defer hub.Close(context.Background())
	if st := hub.Statuses(); len(st) != 1 || !st[0].OK || st[0].ToolCount != 4 {
		t.Fatalf("statuses = %+v, want fixture connected with 4 tools", st)
	}

	touch(t, gate)
	log.wait(t, "fixture:"+string(mcpgate.EventToolsChanged))
	found := false
	for _, tl := range hub.Tools() {
		found = found || tl.QualifiedName == "mcp__fixture__late"
	}
	if !found {
		t.Error("the tool added after connect is not listed")
	}
	if st := hub.Statuses(); st[0].ToolCount != 5 {
		t.Errorf("tool count = %d, want 5", st[0].ToolCount)
	}
}

// TestStdioServerThatExitsWaitsForReconnect: a stdio server that dies is
// marked disconnected and, as in Claude Code, not restarted on its own; a
// call to its tools says how to bring it back, and /mcp reconnect does.
func TestStdioServerThatExitsWaitsForReconnect(t *testing.T) {
	bin := buildFixture(t)
	gate := filepath.Join(t.TempDir(), "exit")
	hub := mcpgate.NewHub()
	log := &eventLog{}
	hub.OnEvent = log.add
	hub.ConnectAll(context.Background(), fixtureConfigs(bin, "--exit-gate", gate))
	defer hub.Close(context.Background())

	touch(t, gate)
	log.wait(t, "fixture:"+string(mcpgate.EventDisconnected))
	if st := hub.Statuses(); st[0].OK || st[0].Error != "disconnected" {
		t.Errorf("status = %+v, want disconnected", st[0])
	}
	_, err := hub.Call(context.Background(), "mcp__fixture__echo", map[string]any{"text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "/mcp reconnect fixture") {
		t.Errorf("call to a disconnected server = %v, want it to name /mcp reconnect", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if log.has("fixture:" + string(mcpgate.EventReconnected)) {
		t.Error("a stdio server was restarted on its own")
	}

	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	st, err := hub.Reconnect(context.Background(), "fixture")
	if err != nil || !st.OK {
		t.Fatalf("Reconnect = %+v, %v", st, err)
	}
	res, err := hub.Call(context.Background(), "mcp__fixture__echo", map[string]any{"text": "back"})
	if err != nil || res.IsError {
		t.Fatalf("call after reconnect = %+v, %v", res, err)
	}
	if _, err := hub.Reconnect(context.Background(), "nope"); err == nil {
		t.Error("reconnecting an unknown server did not fail")
	}
}

// swappable serves whichever handler is current, so a test can restart a
// remote server: the new one knows none of the old sessions.
type swappable struct {
	mu sync.Mutex
	h  http.Handler
}

func (s *swappable) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := s.h
	s.mu.Unlock()
	h.ServeHTTP(w, r)
}

func (s *swappable) restart() {
	s.mu.Lock()
	s.h = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return mcpfixture.NewServer().Server }, nil)
	s.mu.Unlock()
}

// TestRemoteServerReconnectsOnItsOwn: a remote server that restarts loses
// kiln's session; keepalive pings find it, and kiln reconnects on its own,
// as Claude Code does for remote transports.
func TestRemoteServerReconnectsOnItsOwn(t *testing.T) {
	t.Setenv("HARNESS_MCP_KEEPALIVE_MS", "100")
	handler := &swappable{}
	handler.restart()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	hub := mcpgate.NewHub()
	log := &eventLog{}
	hub.OnEvent = log.add
	hub.ConnectAll(context.Background(), map[string]mcpgate.ServerConfig{"remote": {URL: srv.URL}})
	defer hub.Close(context.Background())
	if st := hub.Statuses(); !st[0].OK {
		t.Fatalf("remote did not connect: %+v", st)
	}
	handler.restart()
	log.wait(t, "remote:"+string(mcpgate.EventDisconnected))
	log.wait(t, "remote:"+string(mcpgate.EventReconnected))
	if res, err := hub.Call(context.Background(), "mcp__remote__echo", map[string]any{"text": "x"}); err != nil || res.IsError {
		t.Fatalf("call after the automatic reconnect = %+v, %v", res, err)
	}
	if st := hub.Statuses(); !st[0].OK {
		t.Errorf("status after reconnect = %+v, want connected", st[0])
	}
}
