package cli

import (
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

func TestStepChunkMapsStreamEvents(t *testing.T) {
	c := stepChunk(msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: 1, Delta: "hi"})
	if c["kind"] != "text" || c["text"] != "hi" || c["index"] != 1 {
		t.Errorf("text delta -> %v", c)
	}
	if c := stepChunk(msg.StreamEvent{Type: msg.EventThinkingDelta, Delta: "hm"}); c["kind"] != "thinking" {
		t.Errorf("thinking delta -> %v", c)
	}
	if c := stepChunk(msg.StreamEvent{Type: msg.EventToolCallDelta, Delta: `{"a"`}); c["kind"] != "input" || c["json"] != `{"a"` {
		t.Errorf("tool input delta -> %v", c)
	}
	if c := stepChunk(msg.StreamEvent{Type: msg.EventStart}); c != nil {
		t.Errorf("a start marker became a chunk: %v", c)
	}
}

func TestAppendRowDoors(t *testing.T) {
	user := func(m msg.UserMessage) session.Entry {
		return session.Entry{ID: "e1", Type: session.EntryMessage, Message: m}
	}
	text := msg.Blocks{msg.TextContent{Type: "text", Text: "hi"}}
	cases := []struct {
		name string
		en   session.Entry
		door string
		typ  string
	}{
		{"typed prompt", user(msg.UserMessage{Content: text, KilnTyped: "hi"}), "prompt", "user"},
		{"hook context", user(msg.UserMessage{Content: text, KilnHook: "Stop"}), "hook-context", "user"},
		{"delivery", user(msg.UserMessage{Content: text}), "delivery", "user"},
		{"compaction", session.Entry{ID: "e2", Type: session.EntryCompaction, Summary: "s"}, "compaction", "system"},
	}
	for _, c := range cases {
		row, ok := appendRow("", c.en)
		if !ok || row.Door != c.door || row.Type != c.typ || row.UUID != c.en.ID {
			t.Errorf("%s: row %+v ok %v, want door %s type %s", c.name, row, ok, c.door, c.typ)
		}
	}
	if _, ok := appendRow("", session.Entry{Type: session.EntryCustom}); ok {
		t.Error("a custom entry became a conversation row")
	}
}
