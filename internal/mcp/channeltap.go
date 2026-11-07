package mcp

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andrepato/harness/internal/diag"
)

// channelMethod is the JSON-RPC notification the design studio (and any
// channel server) pushes for each request that needs Claude. It is not a
// method the MCP SDK knows, so the SDK's receiving path rejects it with
// ErrNotHandled before any handler or middleware runs (mcp/shared.go's
// checkRequest). channelTapConn below intercepts it at the transport, one
// level under the SDK, and hides it from the SDK entirely.
const channelMethod = "notifications/claude/channel"

// channelProtocolVersion is the newest legacy MCP revision. A channel server
// is connected with it rather than the modern (2026-07-28) revision: modern
// is stateless and has no session id, and the studio refuses a server as a
// channel over it (it never answers server/discover); legacy's initialize
// handshake mints the session id the channel stream is keyed on.
const channelProtocolVersion = "2025-11-25"

// ChannelEvent is one server-pushed channel notification: the prose the
// model should act on, and the structured fields beside it (for the design
// studio: project, request/decision id, event, status, page).
type ChannelEvent struct {
	Content string
	Meta    map[string]any
}

// channelParams is the shape of channelMethod's params.
type channelParams struct {
	Content string         `json:"content"`
	Meta    map[string]any `json:"meta"`
}

// channelTapTransport wraps a Transport so server-initiated channelMethod
// notifications are delivered to onEvent and never reach the SDK. Every
// other message passes through untouched, so the same connection still
// serves ordinary tool calls on the server.
type channelTapTransport struct {
	inner   sdk.Transport
	onEvent func(ChannelEvent)
}

func (t *channelTapTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &channelTapConn{Connection: conn, onEvent: t.onEvent}, nil
}

// SupportsProtocolVersion forwards to the wrapped transport when it declares
// versions, so wrapping does not change version negotiation.
func (t *channelTapTransport) SupportsProtocolVersion(v string) bool {
	if s, ok := t.inner.(sdk.ProtocolVersionSupporter); ok {
		return s.SupportsProtocolVersion(v)
	}
	return true
}

type channelTapConn struct {
	sdk.Connection
	onEvent func(ChannelEvent)
}

// Read returns the next message the SDK should see, forwarding and
// swallowing any channelMethod notification first so the SDK never sees a
// method it would reject.
func (c *channelTapConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		m, err := c.Connection.Read(ctx)
		if err != nil {
			return nil, err
		}
		req, ok := m.(*jsonrpc.Request)
		if !ok || req.Method != channelMethod {
			return m, nil
		}
		c.forward(req)
		// Swallowed: loop for the next real message.
	}
}

func (c *channelTapConn) forward(req *jsonrpc.Request) {
	var p channelParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		diag.L().Warn("channel: broken notification params", "error", err)
		return
	}
	if c.onEvent != nil {
		c.onEvent(ChannelEvent(p))
	}
}
