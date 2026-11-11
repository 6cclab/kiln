package harness

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func TestConcurrentToolUsageAggregatesOnce(t *testing.T) {
	worker := &tool.Tool{Name: "worker", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Concurrent: true, Execute: func(context.Context, json.RawMessage, tool.Update, tool.Invocation) (tool.Result, error) {
		out := tool.Text("done")
		out.Usage = &msg.Usage{Input: 11, Output: 7}
		return out, nil
	}}
	h, l := newConcurrentTestRig(t, []func() *msg.AssistantMessage{
		func() *msg.AssistantMessage {
			return assistantToolCalls(msg.ToolCall{ID: "a", Name: "worker", Arguments: map[string]any{}}, msg.ToolCall{ID: "b", Name: "worker", Arguments: map[string]any{}})
		},
		func() *msg.AssistantMessage { return assistantText("done") },
	}, worker)
	if _, err := l.Prompt(context.Background(), "work", nil); err != nil {
		t.Fatal(err)
	}
	if got := h.Stats().Usage; got.Input != 22 || got.Output != 14 {
		t.Fatalf("aggregate: %+v", got)
	}
	if _, err := l.Prompt(context.Background(), "continue", nil); err != nil {
		t.Fatal(err)
	}
	if got := h.Stats().Usage; got.Input != 22 || got.Output != 14 {
		t.Fatalf("recounted: %+v", got)
	}
}

func TestSideUsageDoesNotBecomeContextOccupancy(t *testing.T) {
	h, l := newConcurrentTestRig(t, []func() *msg.AssistantMessage{
		func() *msg.AssistantMessage {
			m := assistantText("done")
			m.Usage = msg.Usage{Input: 100, Output: 10}
			return m
		},
	})
	if _, err := l.Prompt(context.Background(), "work", nil); err != nil {
		t.Fatal(err)
	}
	if err := h.RecordSideUsage("worker", msg.Usage{Input: 9000}); err != nil {
		t.Fatal(err)
	}
	if got := h.LastUsage(); got == nil || got.Input != 100 {
		t.Fatalf("context occupancy includes side usage: %+v", got)
	}
	if got := h.Stats().Usage.Input; got != 9100 {
		t.Fatal(got)
	}
}

func TestWorkerBreakdownReplacesAggregateWithoutDoubleCounting(t *testing.T) {
	worker := &tool.Tool{Name: "worker", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage, tool.Update, tool.Invocation) (tool.Result, error) {
		out := tool.Text("done")
		out.Usage = &msg.Usage{Input: 30, Output: 3}
		out.UsageByModel = map[string]msg.Usage{"one/model": {Input: 10, Output: 1}, "two/model": {Input: 20, Output: 2}}
		return out, nil
	}}
	h, l := newConcurrentTestRig(t, []func() *msg.AssistantMessage{func() *msg.AssistantMessage {
		return assistantToolCalls(msg.ToolCall{ID: "a", Name: "worker", Arguments: map[string]any{}})
	}, func() *msg.AssistantMessage { return assistantText("done") }}, worker)
	if _, err := l.Prompt(context.Background(), "work", nil); err != nil {
		t.Fatal(err)
	}
	if got := h.Stats().Usage; got.Input != 30 || got.Output != 3 {
		t.Fatal(got)
	}
	byModel := h.UsageByModel("fake/fake-1")
	if byModel["one/model"].Input != 10 || byModel["two/model"].Input != 20 || byModel["tool · worker"].Input != 0 {
		t.Fatal(byModel)
	}
}
