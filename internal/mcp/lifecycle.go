package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andrepato/harness/internal/diag"
)

// HubEvent is what happened to a server after its first connect
// (Hub.OnEvent).
type HubEvent string

const (
	// EventToolsChanged: the server announced tools/list_changed and its
	// tools were fetched again.
	EventToolsChanged HubEvent = "tools-changed"
	// EventDisconnected: the connection closed. A remote server is being
	// reconnected; a stdio server waits for /mcp reconnect.
	EventDisconnected HubEvent = "disconnected"
	// EventReconnected: a remote server came back on its own.
	EventReconnected HubEvent = "reconnected"
	// EventReconnectFailed: a remote server did not come back after
	// every attempt.
	EventReconnectFailed HubEvent = "reconnect-failed"
)

// reconnectDelays are the waits before each automatic reconnect of a
// remote server. Claude Code reconnects only remote transports on its own
// (a stdio server that exits is marked disconnected until /mcp
// reconnect); kiln does the same.
var reconnectDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// keepAliveInterval is how often a remote server is pinged;
// HARNESS_MCP_KEEPALIVE_MS overrides it (tests).
func keepAliveInterval() time.Duration {
	if ms, err := strconv.Atoi(os.Getenv("HARNESS_MCP_KEEPALIVE_MS")); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return 30 * time.Second
}

func (c *serverConn) close() {
	if c.session != nil {
		_ = c.session.Close()
	}
	killGroup(c.cmd)
}

func toolsOf(server string, tools []*sdk.Tool) []McpTool {
	out := make([]McpTool, 0, len(tools))
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema)
		out = append(out, McpTool{
			Server:        server,
			Name:          t.Name,
			QualifiedName: fmt.Sprintf("mcp__%s__%s", server, t.Name),
			Description:   t.Description,
			InputSchema:   schema,
		})
	}
	return out
}

func (h *Hub) emit(server string, ev HubEvent) {
	h.mu.Lock()
	fn := h.OnEvent
	h.mu.Unlock()
	diag.L().Info("mcp "+string(ev), "server", server)
	if fn != nil {
		fn(server, ev)
	}
}

// watch waits for conn's session to end. If conn is still the server's
// connection then (not replaced by a reconnect, not closed with the hub),
// the server disconnected on its own: its tools stay listed, so a call
// says it is disconnected instead of naming an unknown tool, and a remote
// server is reconnected.
func (h *Hub) watch(name string, conn *serverConn) {
	_ = conn.session.Wait()
	h.mu.Lock()
	if h.closed || h.conns[name] != conn {
		h.mu.Unlock()
		return
	}
	delete(h.conns, name)
	cfg := h.configs[name]
	h.mu.Unlock()
	killGroup(conn.cmd)
	h.setStatus(ServerStatus{Name: name, OK: false, Error: "disconnected", Detail: "the connection closed"})
	h.emit(name, EventDisconnected)
	if !spawnsProcess(cfg) {
		h.reconnectLoop(name, cfg)
	}
}

// reconnectLoop redials a remote server after each of reconnectDelays,
// stopping when it connects, the hub closes, or a manual reconnect has
// already brought it back.
func (h *Hub) reconnectLoop(name string, cfg ServerConfig) {
	for _, d := range reconnectDelays {
		time.Sleep(d)
		h.mu.Lock()
		done := h.closed || h.conns[name] != nil
		h.mu.Unlock()
		if done {
			return
		}
		st := h.dial(context.Background(), name, cfg, connectTimeout())
		if st.OK {
			h.setStatus(st)
			h.emit(name, EventReconnected)
			return
		}
		diag.L().Info("mcp reconnect attempt failed", "server", name, "reason", st.Error)
	}
	h.emit(name, EventReconnectFailed)
}

// refreshTools fetches a server's tools again after tools/list_changed.
// A failed fetch keeps the previous tools, as Claude Code does; a session
// that is no longer the server's connection is ignored.
func (h *Hub) refreshTools(name string, session *sdk.ClientSession) {
	if session == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout())
	res, err := session.ListTools(ctx, nil)
	cancel()
	if err != nil {
		diag.L().Warn("mcp tools/list_changed refresh failed; keeping previous tools", "server", name, "err", err)
		return
	}
	h.mu.Lock()
	conn := h.conns[name]
	if conn == nil || conn.session != session {
		h.mu.Unlock()
		return
	}
	h.tools[name] = toolsOf(name, res.Tools)
	for i := range h.statuses {
		if h.statuses[i].Name == name {
			h.statuses[i].ToolCount = len(h.tools[name])
		}
	}
	h.mu.Unlock()
	h.emit(name, EventToolsChanged)
}

// Reconnect connects a configured server again (/mcp reconnect): the
// current connection, if any, is closed first. It returns the outcome.
func (h *Hub) Reconnect(ctx context.Context, name string) (ServerStatus, error) {
	h.mu.Lock()
	cfg, ok := h.configs[name]
	old := h.conns[name]
	delete(h.conns, name)
	h.mu.Unlock()
	if !ok {
		return ServerStatus{}, fmt.Errorf("there's no MCP server named %q", name)
	}
	if old != nil {
		old.close()
	}
	st := h.dial(ctx, name, cfg, connectTimeout())
	h.setStatus(st)
	return st, nil
}

// SetOnEvent sets OnEvent; safe once servers are connecting.
func (h *Hub) SetOnEvent(fn func(server string, ev HubEvent)) {
	h.mu.Lock()
	h.OnEvent = fn
	h.mu.Unlock()
}

// Remote reports whether a configured server is reached over the network
// (and so reconnects on its own) rather than started as a process.
func (h *Hub) Remote(name string) bool {
	h.mu.Lock()
	cfg, ok := h.configs[name]
	h.mu.Unlock()
	return ok && !spawnsProcess(cfg)
}

// ServerNames are the configured servers, for /mcp reconnect all.
func (h *Hub) ServerNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.configs))
	for n := range h.configs {
		names = append(names, n)
	}
	return names
}
