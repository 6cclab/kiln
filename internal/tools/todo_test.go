package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func TestTodoWriteReplacesTheWholeList(t *testing.T) {
	var stored []TodoItem
	tl := TodoTool(func(items []TodoItem) { stored = items })

	args := json.RawMessage(`{"todos":[
		{"content":"a","status":"completed"},
		{"content":"b","status":"in_progress"},
		{"content":"c","status":"pending"}
	]}`)
	res, err := tl.Execute(context.Background(), args, func(tool.Result) {}, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 {
		t.Fatalf("got %d stored items, want 3", len(stored))
	}

	// The result echoes a count and the in-progress item, not the list.
	got := msg.TextOf(res.Content)
	if got != "3 todos, 1 done. Now: b" {
		t.Fatalf("got %q", got)
	}
}

func TestTodoWriteDropsInvalidEntries(t *testing.T) {
	var stored []TodoItem
	tl := TodoTool(func(items []TodoItem) { stored = items })

	args := json.RawMessage(`{"todos":[
		{"content":"","status":"pending"},
		{"content":"ok","status":"bogus"},
		{"content":"keep","status":"pending"}
	]}`)
	if _, err := tl.Execute(context.Background(), args, func(tool.Result) {}, tool.Invocation{}); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Content != "keep" {
		t.Fatalf("got %+v", stored)
	}
}
