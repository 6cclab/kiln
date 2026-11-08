package mcp

import (
	"io"
	"strings"
	"testing"
)

// filterChannelSSE drops notifications/claude/channel events (forwarding them)
// and passes every other SSE byte through verbatim.
func TestFilterChannelSSE(t *testing.T) {
	// A ping comment, a channel notification, and an unrelated notification.
	in := ": ping\n\n" +
		`data: {"jsonrpc":"2.0","method":"notifications/claude/channel","params":{"content":"do it","meta":{"request":"c_1"}}}` + "\n\n" +
		`data: {"jsonrpc":"2.0","method":"notifications/message","params":{"x":1}}` + "\n\n"

	var got []ChannelEvent
	rc := filterChannelSSE(io.NopCloser(strings.NewReader(in)), func(ev ChannelEvent) { got = append(got, ev) })
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}

	// The channel event was forwarded...
	if len(got) != 1 || got[0].Content != "do it" || got[0].Meta["request"] != "c_1" {
		t.Fatalf("forwarded events = %+v, want one {do it, c_1}", got)
	}
	// ...and removed from the passed-through body, which keeps the ping and the
	// unrelated notification verbatim.
	o := string(out)
	if strings.Contains(o, "claude/channel") {
		t.Errorf("channel event not filtered out of body:\n%s", o)
	}
	if !strings.Contains(o, ": ping") || !strings.Contains(o, "notifications/message") {
		t.Errorf("non-channel events not passed through:\n%s", o)
	}
}

// A data payload that isn't a channel notification is left for passthrough.
func TestChannelData(t *testing.T) {
	if p, ok := channelData([]byte(`{"method":"notifications/claude/channel","params":{"content":"hi","meta":{"a":"b"}}}`)); !ok || p.Content != "hi" {
		t.Errorf("channel notif not recognised: %+v ok=%v", p, ok)
	}
	if _, ok := channelData([]byte(`{"method":"notifications/message"}`)); ok {
		t.Error("non-channel method should not be recognised")
	}
	if _, ok := channelData([]byte("not json")); ok {
		t.Error("non-json should not be recognised")
	}
	if _, ok := channelData([]byte("")); ok {
		t.Error("empty should not be recognised")
	}
}

func TestSSEData(t *testing.T) {
	if v, ok := sseData([]byte("data: hello\n")); !ok || string(v) != "hello" {
		t.Errorf("sseData = %q ok=%v, want hello", v, ok)
	}
	if v, ok := sseData([]byte("data:no-space\n")); !ok || string(v) != "no-space" {
		t.Errorf("sseData = %q, want no-space", v)
	}
	if _, ok := sseData([]byte(": comment\n")); ok {
		t.Error("a comment line is not data")
	}
}
