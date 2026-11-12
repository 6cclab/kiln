package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tool"
)

func TestProgressGuard(t *testing.T) {
	call := msg.ToolCall{Name: "read", Arguments: map[string]any{"path": "missing"}}
	failure := msg.ToolResultMessage{IsError: true, Content: msg.Blocks{msg.Text("not found")}}
	changed := msg.ToolResultMessage{IsError: true, Content: msg.Blocks{msg.Text("permission denied")}}
	success := msg.ToolResultMessage{Content: msg.Blocks{msg.Text("new data")}}
	for _, tc := range []struct {
		name    string
		results []msg.ToolResultMessage
		wantAt  int
	}{
		{"repeated failure", []msg.ToolResultMessage{failure, failure, failure}, 3},
		{"changed result", []msg.ToolResultMessage{failure, changed, failure}, 0},
		{"success resets", []msg.ToolResultMessage{failure, success, failure}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := progressGuard{limit: 3}
			for i, result := range tc.results {
				err := guard.observe(call, result)
				if tc.wantAt == i+1 {
					if err == nil || !strings.Contains(err.Error(), "repeated") {
						t.Fatalf("observation %d: expected bounded stop, got %v", i+1, err)
					}
				} else if err != nil {
					t.Fatalf("observation %d: %v", i+1, err)
				}
			}
		})
	}
}

func TestProgressGuardPairsSkippedCallsWithoutEffects(t *testing.T) {
	read1 := msg.NewToolCall("read1", "read", map[string]any{"path": "missing.txt"})
	read2 := msg.NewToolCall("read2", "read", map[string]any{"path": "missing.txt"})
	write := msg.NewToolCall("write3", "write", map[string]any{"path": "guard-marker.txt", "content": "must not be written"})
	turns := []func() *msg.AssistantMessage{
		func() *msg.AssistantMessage { return assistantToolCalls(read1, read2, write) },
		func() *msg.AssistantMessage { return assistantText("recovered") },
	}
	h, lane := newConcurrentTestRig(t, turns)
	h.opts.ProgressLimit = 2
	beforeCalls, afterCalls := 0, 0
	h.Hooks().OnBeforeTool(func(_ context.Context, _ msg.ToolCall) (BeforeToolResult, error) {
		beforeCalls++
		return BeforeToolResult{}, nil
	})
	h.Hooks().OnAfterTool(func(_ context.Context, _ msg.ToolCall, _ *msg.ToolResultMessage) error {
		afterCalls++
		return nil
	})
	result, err := lane.Prompt(context.Background(), "try", nil)
	if result.Status != StatusFailed || err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("first prompt = %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(h.opts.Cwd, "guard-marker.txt")); !os.IsNotExist(err) {
		t.Fatalf("later write executed: %v", err)
	}
	if beforeCalls != 2 || afterCalls != 2 {
		t.Fatalf("skipped call ran hooks: before=%d after=%d", beforeCalls, afterCalls)
	}
	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]msg.ToolResultMessage{}
	for _, entry := range entries {
		if entry.Type != session.EntryMessage {
			continue
		}
		if tr, ok := entry.Message.(msg.ToolResultMessage); ok {
			results[tr.ToolCallID] = tr
		}
	}
	if len(results) != 3 {
		t.Fatalf("paired results = %v, want all 3 tool call IDs", results)
	}
	if !results["write3"].IsError || !strings.Contains(results["write3"].Content[0].(msg.TextContent).Text, "did not run") || results["write3"].Usage != nil || len(results["write3"].UsageByModel) != 0 {
		t.Fatalf("skipped result = %+v", results["write3"])
	}
	second, err := lane.Prompt(context.Background(), "try again", nil)
	if err != nil || second.Status != StatusCompleted {
		t.Fatalf("second prompt = %#v, %v", second, err)
	}
}

func TestProgressGuardUsesPostHookArguments(t *testing.T) {
	constantFailure := &tool.Tool{
		Name: "constant_failure", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
		Execute: func(context.Context, json.RawMessage, tool.Update, tool.Invocation) (tool.Result, error) {
			return tool.Errorf("same failure"), nil
		},
	}
	call1 := msg.NewToolCall("one", "constant_failure", map[string]any{"value": "original"})
	call2 := msg.NewToolCall("two", "constant_failure", map[string]any{"value": "original"})
	turns := []func() *msg.AssistantMessage{
		func() *msg.AssistantMessage { return assistantToolCalls(call1, call2) },
		func() *msg.AssistantMessage { return assistantText("done") },
	}
	h, lane := newConcurrentTestRig(t, turns, constantFailure)
	h.opts.ProgressLimit = 2
	h.Hooks().OnBeforeTool(func(_ context.Context, call msg.ToolCall) (BeforeToolResult, error) {
		return BeforeToolResult{RewrittenArgs: json.RawMessage(`{"value":"` + call.ID + `"}`)}, nil
	})
	result, err := lane.Prompt(context.Background(), "go", nil)
	if err != nil || result.Status != StatusCompleted {
		t.Fatalf("changed effective args should reset guard: %#v, %v", result, err)
	}
}

func TestProgressGuardStopsRepeatedFailedToolOperation(t *testing.T) {
	script := `model: faux-1
steps:
  - tool_call: {name: read, args: {path: missing.txt}, id: c1}
  - on_tool_result: c1
    then:
      - tool_call: {name: read, args: {path: missing.txt}, id: c2}
  - on_tool_result: c2
    then:
      - text: "should not be reached"
`
	rig := newTestRigWith(t, script, nil, nil, func(opts *Options, _ *provider.Registry) {
		opts.ProgressLimit = 2
	})
	result, err := rig.mustLane("main").Prompt(context.Background(), "read the file", nil)
	if result.Status != StatusFailed || err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("run result = %#v, err = %v", result, err)
	}
	if got := len(rig.Faux.Requests()); got != 2 {
		t.Fatalf("model requests = %d, want 2", got)
	}
}

func TestProgressGuardArgumentsAndDisabled(t *testing.T) {
	result := msg.ToolResultMessage{IsError: true, Content: msg.Blocks{msg.Text("failed")}}
	guard := progressGuard{limit: 2}
	for _, path := range []string{"a", "b", "a"} {
		if err := guard.observe(msg.ToolCall{Name: "read", Arguments: map[string]any{"path": path}}, result); err != nil {
			t.Fatal(err)
		}
	}
	guard.limit = 0
	for range 10 {
		if err := guard.observe(msg.ToolCall{Name: "read", Arguments: map[string]any{"path": "a"}}, result); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProgressGuardIgnoresPerCallBookkeeping(t *testing.T) {
	guard := progressGuard{limit: 2}
	call := msg.ToolCall{Name: "bash", Arguments: map[string]any{"command": "exit 1"}}
	for i, details := range []string{`{"taskVerificationAttempt":{"callId":"one","command":"exit 1"}}`, `{"taskVerificationAttempt":{"callId":"two","command":"exit 1"}}`} {
		err := guard.observe(call, msg.ToolResultMessage{IsError: true, Content: msg.Blocks{msg.Text("Command exited with code 1")}, Details: []byte(details)})
		if (err != nil) != (i == 1) {
			t.Fatalf("step %d: %v", i, err)
		}
	}
}
