package mcp

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// scriptedConn is a fake transport Connection that returns a fixed sequence
// of messages from Read, then io.EOF.
type scriptedConn struct {
	msgs []jsonrpc.Message
	i    int
}

func (c *scriptedConn) Read(context.Context) (jsonrpc.Message, error) {
	if c.i >= len(c.msgs) {
		return nil, io.EOF
	}
	m := c.msgs[c.i]
	c.i++
	return m, nil
}
func (c *scriptedConn) Write(context.Context, jsonrpc.Message) error { return nil }
func (c *scriptedConn) Close() error                                 { return nil }
func (c *scriptedConn) SessionID() string                            { return "test" }

func channelReq(t *testing.T, content string, meta map[string]any) *jsonrpc.Request {
	t.Helper()
	params, err := json.Marshal(channelParams{Content: content, Meta: meta})
	if err != nil {
		t.Fatal(err)
	}
	return &jsonrpc.Request{Method: channelMethod, Params: params}
}

// A channelMethod notification is parsed, forwarded to onEvent and swallowed;
// the messages around it (a tool result, an unrelated notification) pass
// through in order, so the SDK never sees the method it would reject.
func TestChannelTap_InterceptsAndPassesThrough(t *testing.T) {
	var got []ChannelEvent
	toolResult := &jsonrpc.Response{ID: mustID(t, "r1")}
	otherNotif := &jsonrpc.Request{Method: "notifications/message"}

	inner := &scriptedConn{msgs: []jsonrpc.Message{
		toolResult,
		channelReq(t, "A new request", map[string]any{"project": "margin", "request": "c_1", "event": "request"}),
		otherNotif,
	}}
	conn := &channelTapConn{Connection: inner, onEvent: func(ev ChannelEvent) { got = append(got, ev) }}

	// First Read returns the tool response untouched.
	if m, err := conn.Read(context.Background()); err != nil || m != toolResult {
		t.Fatalf("first Read = %v, %v; want the tool response", m, err)
	}
	// Second Read skips the swallowed channel notification and returns the
	// next real message (the other notification).
	if m, err := conn.Read(context.Background()); err != nil || m != otherNotif {
		t.Fatalf("second Read = %v, %v; want the passed-through notification", m, err)
	}
	// Stream drained.
	if _, err := conn.Read(context.Background()); err != io.EOF {
		t.Fatalf("third Read err = %v; want io.EOF", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	if got[0].Content != "A new request" {
		t.Errorf("Content = %q, want %q", got[0].Content, "A new request")
	}
	if got[0].Meta["request"] != "c_1" || got[0].Meta["event"] != "request" {
		t.Errorf("Meta = %v, want request=c_1 event=request", got[0].Meta)
	}
}

// A channelMethod notification with broken params is dropped (no event, no
// panic) and still swallowed.
func TestChannelTap_BrokenParamsDropped(t *testing.T) {
	var got []ChannelEvent
	bad := &jsonrpc.Request{Method: channelMethod, Params: json.RawMessage(`"not an object"`)}
	after := &jsonrpc.Response{ID: mustID(t, "r2")}
	conn := &channelTapConn{
		Connection: &scriptedConn{msgs: []jsonrpc.Message{bad, after}},
		onEvent:    func(ev ChannelEvent) { got = append(got, ev) },
	}
	if m, err := conn.Read(context.Background()); err != nil || m != after {
		t.Fatalf("Read = %v, %v; want the message after the broken one", m, err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events, want 0 for broken params", len(got))
	}
}

func mustID(t *testing.T, v any) jsonrpc.ID {
	t.Helper()
	id, err := jsonrpc.MakeID(v)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
