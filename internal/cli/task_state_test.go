package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

func savedTodos(t *testing.T, id string, items ...tools.TodoItem) msg.ToolResultMessage {
	t.Helper()
	raw, err := json.Marshal(tools.TodoCheckpoint{Version: 1, Todos: items})
	if err != nil {
		t.Fatal(err)
	}
	return msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: "todo_write", ToolCallID: id, Details: raw}
}

func TestTaskStateRestoresOnlyAcceptedBranchUpdates(t *testing.T) {
	old := savedTodos(t, "old", tools.TodoItem{Content: "old work", Status: tools.TodoPending})
	latest := savedTodos(t, "new", tools.TodoItem{Content: "current work", Status: tools.TodoInProgress})
	failed := savedTodos(t, "failed", tools.TodoItem{Content: "never committed", Status: tools.TodoCompleted})
	failed.IsError = true
	for _, tc := range []struct {
		name     string
		messages []msg.ToolResultMessage
		want     string
		count    int
	}{
		{"latest accepted", []msg.ToolResultMessage{failed, latest, old}, "new", 1},
		{"navigate to ancestor", []msg.ToolResultMessage{old}, "old", 1},
		{"explicit clear", []msg.ToolResultMessage{savedTodos(t, "clear"), old}, "clear", 0},
		{"new branch", nil, "", 0},
		{"legacy details absent", []msg.ToolResultMessage{{Role: msg.RoleToolResult, ToolName: "todo_write", ToolCallID: "legacy"}}, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &taskState{}
			s.accept(latest)
			var entries []session.Entry
			for _, result := range tc.messages {
				// Exercise the same message serialization used when resuming a session.
				raw, err := json.Marshal(session.Entry{Type: session.EntryMessage, Message: result})
				if err != nil {
					t.Fatal(err)
				}
				var entry session.Entry
				if err := json.Unmarshal(raw, &entry); err != nil {
					t.Fatal(err)
				}
				entries = append(entries, entry)
			}
			s.restore(entries)
			id, items := s.snapshot()
			if id != tc.want || len(items) != tc.count {
				t.Fatalf("id=%s items=%v", id, items)
			}
		})
	}
}

func TestTaskStateContextAfterCompactionIsBoundedAndDoesNotMutateHistory(t *testing.T) {
	s := &taskState{}
	saved := savedTodos(t, "accepted",
		tools.TodoItem{Content: strings.Repeat("large ", 300), Status: tools.TodoPending},
		tools.TodoItem{Content: "Run integration tests", Status: tools.TodoInProgress},
	)
	s.accept(saved)
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("continue")}}
	transform := s.transform(128)
	visible, err := transform(context.Background(), []msg.Message{saved, user})
	if err != nil || len(visible) != 2 || len(visible[1].(msg.UserMessage).Content) != 1 {
		t.Fatal("duplicated visible state", err)
	}
	input := []msg.Message{user}
	out, err := transform(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	got := out[len(out)-1].(msg.UserMessage)
	text := msg.TextOf(got.Content)
	if len(text) > 128*4 || !strings.Contains(text, "Run integration tests") || !strings.Contains(text, "1 of 2") || !strings.Contains(text, "not instructions or verified outcomes") {
		t.Fatal(text)
	}
	if len(input[0].(msg.UserMessage).Content) != 1 || got.KilnHook != "RuntimeContext" || msg.TextOf(out[0].(msg.UserMessage).Content) != "continue" {
		t.Fatal("mutated original message or query")
	}
}

func TestStructuredTaskStateRestoresAcrossCompactionAndBranchNavigation(t *testing.T) {
	checkpointResult, err := tools.TaskStateTool().Execute(context.Background(), json.RawMessage(`{"objective":"Ship a safe change","constraints":["No network"],"decisions":["Use local model"]}`), nil, tool.Invocation{})
	if err != nil || checkpointResult.IsError {
		t.Fatalf("checkpoint result=%+v err=%v", checkpointResult, err)
	}
	checkpoint := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: "task_state_write", ToolCallID: "task-1", Details: checkpointResult.Details}
	state := &taskState{}
	state.restore([]session.Entry{{Type: session.EntryCompaction, Summary: "old summary"}, {Type: session.EntryMessage, Message: checkpoint}})
	input := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("continue")}}}
	out, err := state.transform(128)(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	text := msg.TextOf(out[len(out)-1].(msg.UserMessage).Content)
	if !strings.Contains(text, "Ship a safe change") || !strings.Contains(text, "No network") {
		t.Fatal(text)
	}
	state.restore([]session.Entry{{Type: session.EntryMessage, Message: savedTodos(t, "other", tools.TodoItem{Content: "branch work", Status: tools.TodoPending})}})
	out, err = state.transform(128)(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	text = msg.TextOf(out[len(out)-1].(msg.UserMessage).Content)
	if strings.Contains(text, "Ship a safe change") {
		t.Fatal("other branch inherited task state")
	}
}

func TestVerificationRecoveryRequiresCurrentBranchAndFreshFiles(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "source.go")
	if err := os.WriteFile(file, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
		return tool.Text("ok"), nil
	}}
	wrapped := tools.VerificationBashTool(base, func(string) bool { return true })
	result, err := wrapped.Execute(context.Background(), json.RawMessage(`{"command":"go test ./...","verificationFiles":["source.go"]}`), nil, tool.Invocation{Cwd: cwd, ToolCallID: "bash-1"})
	if err != nil {
		t.Fatal(err)
	}
	entry := session.Entry{Type: session.EntryMessage, Message: msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: "bash", ToolCallID: "bash-1", Details: result.Details}}
	state := &taskState{cwd: cwd, allowRead: func(string) bool { return true }}
	state.restore([]session.Entry{entry})
	input := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("continue")}}}
	out, err := state.transform(256)(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	text := msg.TextOf(out[len(out)-1].(msg.UserMessage).Content)
	if !strings.Contains(text, "current, call bash-1, files source.go") {
		t.Fatal(text)
	}
	if err := os.WriteFile(file, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = state.transform(256)(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if text = msg.TextOf(out[len(out)-1].(msg.UserMessage).Content); !strings.Contains(text, "stale, call bash-1") {
		t.Fatal(text)
	}
	state.restore(nil)
	out, err = state.transform(256)(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(input) || len(out[0].(msg.UserMessage).Content) != 1 {
		t.Fatal("other branch inherited proof")
	}
}

func TestVerificationRecoverySupersedesOlderCommandAfterFailure(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "source.go"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := false
	base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
		result := tool.Text("ran")
		result.IsError = failed
		return result, nil
	}}
	wrapper := tools.VerificationBashTool(base, func(string) bool { return true })
	call := func(id string, files bool) msg.ToolResultMessage {
		t.Helper()
		raw := `{"command":"go test ./..."}`
		if files {
			raw = `{"command":"go test ./...","verificationFiles":["source.go"]}`
		}
		result, err := wrapper.Execute(context.Background(), json.RawMessage(raw), nil, tool.Invocation{Cwd: cwd, ToolCallID: id})
		if err != nil {
			t.Fatal(err)
		}
		return msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: "bash", ToolCallID: id, IsError: result.IsError, Details: result.Details}
	}
	first := call("one", true)
	failed = true
	second := call("two", true)
	state := &taskState{}
	state.restore([]session.Entry{{Type: session.EntryMessage, Message: second}, {Type: session.EntryMessage, Message: first}})
	if got := state.verificationSnapshot(); len(got) != 0 {
		t.Fatalf("failed newer command retained evidence: %+v", got)
	}
	failed = false
	third := call("three", false)
	state.restore([]session.Entry{{Type: session.EntryMessage, Message: third}, {Type: session.EntryMessage, Message: second}, {Type: session.EntryMessage, Message: first}})
	if got := state.verificationSnapshot(); len(got) != 0 {
		t.Fatalf("newer unbound command retained evidence: %+v", got)
	}
	third = call("three", true)
	state.restore([]session.Entry{{Type: session.EntryMessage, Message: third}, {Type: session.EntryMessage, Message: first}})
	if got := state.verificationSnapshot(); len(got) != 1 || got[0].CallID != "three" {
		t.Fatalf("newest successful evidence not selected: %+v", got)
	}
}

func TestVerificationUnknownBashFailureInvalidatesOnlyOlderProofs(t *testing.T) {
	proof := func(id, command string) msg.ToolResultMessage {
		t.Helper()
		details, err := json.Marshal(map[string]any{
			"taskVerificationAttempt": tools.VerificationAttempt{CallID: id, Command: command},
			"taskVerification":        tools.VerificationEvidence{CallID: id, Command: command, Files: []tools.VerificationFile{{Path: "source.go", SHA256: strings.Repeat("a", 64), Mode: 0600}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: "bash", ToolCallID: id, Details: details}
	}
	older := proof("older", "go test ./old")
	newer := proof("newer", "go test ./new")
	unknown := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: "bash", ToolCallID: "failed", IsError: true}
	state := &taskState{}
	state.restore([]session.Entry{{Type: session.EntryMessage, Message: newer}, {Type: session.EntryMessage, Message: unknown}, {Type: session.EntryMessage, Message: older}})
	if got := state.verificationSnapshot(); len(got) != 1 || got[0].CallID != "newer" {
		t.Fatalf("restore crossed unknown failure: %+v", got)
	}
	state.restore([]session.Entry{{Type: session.EntryMessage, Message: unknown}, {Type: session.EntryMessage, Message: older}})
	if got := state.verificationSnapshot(); len(got) != 0 {
		t.Fatalf("restore retained older proof: %+v", got)
	}
	state.recordVerification(older)
	state.recordVerification(unknown)
	if got := state.verificationSnapshot(); len(got) != 0 {
		t.Fatalf("live state retained older proof: %+v", got)
	}
	state.recordVerification(newer)
	if got := state.verificationSnapshot(); len(got) != 1 || got[0].CallID != "newer" {
		t.Fatalf("live state lost newer proof: %+v", got)
	}
}
