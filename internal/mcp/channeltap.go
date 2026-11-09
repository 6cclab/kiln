package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/diag"
)

// channelMethod is the JSON-RPC notification the design studio (and any channel
// server) pushes on its standalone GET SSE stream for each request that needs
// Claude. It is not a method the MCP SDK knows, so it must never reach the SDK
// (its receiving path would reject it). We therefore filter it out of the SSE
// response body at the http layer — see channelTapRoundTripper.
//
// An earlier version wrapped the MCP Connection to intercept it at Read. That
// was wrong: wrapping the Connection hides the SDK's concrete
// *streamableClientConn, so the SDK's `conn.(clientConnection)` assertion fails,
// sessionUpdated() never runs, and the standalone GET stream that carries these
// pushes is never opened. Filtering the body leaves the connection untouched.
const channelMethod = "notifications/claude/channel"

// channelProtocolVersion is the newest legacy MCP revision. A channel server is
// connected with it rather than the modern (2026-07-28) revision: modern is
// stateless and removes the standalone GET SSE stream (SEP-2575); legacy keeps
// it, which is where channel pushes arrive.
const channelProtocolVersion = "2025-11-25"

// maxChannelEvent caps one buffered SSE event, so a malformed stream can't grow
// the buffer without bound.
const maxChannelEvent = 1 << 20

// ChannelEvent is one server-pushed channel notification: the prose the model
// should act on, and the structured fields beside it (for the design studio:
// project, request/decision id, event, status, page).
type ChannelEvent struct {
	Content string
	Meta    map[string]any
}

type channelParams struct {
	Content string         `json:"content"`
	Meta    map[string]any `json:"meta"`
}

// fileURI renders an absolute path as a file:// URI (file:///abs/path), with
// the path percent-encoded, the form a roots/list answer carries. The design
// studio reconstructs the path with decodeURIComponent(uri.slice(7)), so a
// path with spaces or other reserved bytes round-trips.
func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// channelTapRoundTripper wraps an http.RoundTripper so a channel server's
// standalone SSE stream has its notifications/claude/channel events filtered out
// (and forwarded to onEvent) before the SDK sees the body. Every other response,
// and every other SSE event, passes through untouched.
type channelTapRoundTripper struct {
	inner   http.RoundTripper
	onEvent func(ChannelEvent)
}

func (rt channelTapRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := rt.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	resp, err := inner.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body = filterChannelSSE(resp.Body, rt.onEvent)
	}
	return resp, nil
}

// filterChannelSSE streams src, dropping whole SSE events whose data is a
// notifications/claude/channel notification (forwarding each to onEvent) and
// passing every other byte through verbatim. It never buffers more than one
// event.
func filterChannelSSE(src io.ReadCloser, onEvent func(ChannelEvent)) io.ReadCloser {
	pr, pw := io.Pipe()
	crash.Go(func() {
		defer src.Close()
		r := bufio.NewReader(src)
		var raw bytes.Buffer  // the current event's bytes, for passthrough
		var data bytes.Buffer // the current event's concatenated data payload
		flush := func() error {
			defer func() { raw.Reset(); data.Reset() }()
			if raw.Len() == 0 {
				return nil
			}
			if p, ok := channelData(data.Bytes()); ok {
				if onEvent != nil {
					onEvent(ChannelEvent(p))
				}
				return nil // drop: the SDK never sees it
			}
			_, err := pw.Write(raw.Bytes())
			return err
		}
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				if raw.Len()+len(line) > maxChannelEvent {
					// Oversized event: stop filtering, pass the rest through.
					pw.Write(raw.Bytes())
					pw.Write(line)
					_, cerr := io.Copy(pw, r)
					pw.CloseWithError(cerr)
					return
				}
				raw.Write(line)
				if d, ok := sseData(line); ok {
					if data.Len() > 0 {
						data.WriteByte('\n')
					}
					data.Write(d)
				}
				if isBlankLine(line) {
					if ferr := flush(); ferr != nil {
						src.Close()
						return
					}
				}
			}
			if err != nil {
				_ = flush()
				pw.CloseWithError(err)
				return
			}
		}
	})
	return pr
}

// sseData returns the value of a "data:" SSE field line (minus the optional
// single leading space), or ok=false for any other line.
func sseData(line []byte) (value []byte, ok bool) {
	line = bytes.TrimRight(line, "\r\n")
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	v := line[len("data:"):]
	if len(v) > 0 && v[0] == ' ' {
		v = v[1:]
	}
	return v, true
}

func isBlankLine(line []byte) bool {
	return len(bytes.TrimRight(line, "\r\n")) == 0
}

// channelData parses an SSE event's data payload as a notifications/claude/channel
// notification, returning its params or ok=false if it is anything else.
func channelData(data []byte) (channelParams, bool) {
	if len(bytes.TrimSpace(data)) == 0 {
		return channelParams{}, false
	}
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.Method != channelMethod {
		return channelParams{}, false
	}
	var p channelParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		diag.L().Warn("channel: broken notification params", "error", err)
		return channelParams{}, false
	}
	return p, true
}
