package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// NOTE: TestChannelPushReachesOnChannel currently FAILS. It is the regression
// test that caught a real bug: the channel tap wraps the MCP Connection, which
// hides the SDK's concrete *streamableClientConn, so cs.mcpConn.(clientConnection)
// fails and sessionUpdated()/the standalone GET SSE stream never fire — no pushes
// arrive. The tap must move to an http RoundTripper that filters the SSE body
// instead of wrapping the Connection. See docs/design-mcp-oauth.md / the report.
//
// rawMCPServer is a minimal MCP Streamable-HTTP server: enough of the legacy
// handshake for the go-sdk client to connect, plus a standalone GET SSE stream
// that pushes one notifications/claude/channel. It exists because the SDK's own
// server can't emit a custom notification method, so the channel push can only
// be exercised with a hand-rolled server.
func rawMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	const sessionID = "test-session"
	var mu sync.Mutex
	pushed := false

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var req struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Method  string          `json:"method"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			w.Header().Set("Mcp-Session-Id", sessionID)
			switch req.Method {
			case "initialize":
				writeJSONRPC(w, req.ID, map[string]any{
					"protocolVersion": "2025-11-25",
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "raw", "version": "0"},
				})
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
			case "tools/list":
				writeJSONRPC(w, req.ID, map[string]any{"tools": []any{}})
			default:
				writeJSONRPC(w, req.ID, map[string]any{})
			}
		case http.MethodGet:
			// The standalone SSE stream: push one channel notification, then hold.
			fl, ok := w.(http.Flusher)
			if !ok {
				t.Error("no flusher")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fl.Flush()
			mu.Lock()
			already := pushed
			pushed = true
			mu.Unlock()
			if !already {
				notif := `{"jsonrpc":"2.0","method":"notifications/claude/channel","params":{"content":"a studio request","meta":{"project":"margin","request":"c_1","event":"request"}}}`
				fmt.Fprintf(w, "data: %s\n\n", notif)
				fl.Flush()
			}
			<-r.Context().Done()
		}
	}))
}

func writeJSONRPC(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	w.Write(data)
}

// End to end: a channel server's notifications/claude/channel push reaches the
// hub's OnChannel, through the real sdk client + the channelTap wrapper.
func TestChannelPushReachesOnChannel(t *testing.T) {
	srv := rawMCPServer(t)
	defer srv.Close()

	h := NewHub()
	h.Channels = map[string]string{"studio": "design-studio"}
	got := make(chan ChannelEvent, 1)
	h.OnChannel = func(server, source string, ev ChannelEvent) {
		if server == "studio" && source == "design-studio" {
			select {
			case got <- ev:
			default:
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h.ConnectAll(ctx, map[string]ServerConfig{"studio": {Type: "http", URL: srv.URL}})

	select {
	case ev := <-got:
		if ev.Content != "a studio request" || ev.Meta["request"] != "c_1" {
			t.Errorf("event = %+v, want the pushed request", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel push never reached OnChannel")
	}
}
