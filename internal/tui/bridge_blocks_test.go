package tui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
)

// waitForOneSent polls f.snapshot() until at least one message has been
// sent, or fails after a short deadline — handleEvent's Send/Commit calls
// land on the bridge's own committer goroutine, not the caller's.
func waitForOneSent(t *testing.T, f *fakeSink) any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, sent := f.snapshot(); len(sent) >= 1 {
			return sent[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for a sent message")
	return nil
}

func TestBridge_ToolEndMeta_ApprovedAndElapsed(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	b.handleEvent(harness.Event{Type: harness.EventToolStart, ToolCallID: "t1", ToolName: "bash"}, ts, 4000)
	time.Sleep(2 * time.Millisecond)
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "bash",
		ToolArgs:          map[string]any{"command": "npm test"},
		ToolResult:        &msg.ToolResultMessage{Content: msg.Blocks{msg.Text("ok")}},
		PermissionOutcome: string(permission.OutcomeApproved),
	}, ts, 4000)

	got := waitForOneSent(t, f)
	call, ok := got.(msgCommitToolCall)
	if !ok {
		t.Fatalf("sent = %T, want msgCommitToolCall", got)
	}
	if call.View.Meta == "" {
		t.Fatal("Meta is empty, want \"approved · Ns\"")
	}
	if len(call.View.Meta) < len("approved") || call.View.Meta[:len("approved")] != "approved" {
		t.Errorf("Meta = %q, want it to start with \"approved\"", call.View.Meta)
	}
}

func TestBridge_TodoWrite_SendsMsgTodosNotToolCall(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	args := map[string]any{"todos": []any{
		map[string]any{"content": "step one", "status": "completed"},
		map[string]any{"content": "step two", "status": "in_progress"},
	}}
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "todo_write",
		ToolArgs: args, ToolResult: &msg.ToolResultMessage{},
	}, ts, 4000)

	got := waitForOneSent(t, f)
	todos, ok := got.(MsgTodos)
	if !ok {
		t.Fatalf("sent = %T, want MsgTodos (no tool block for todo_write)", got)
	}
	if len(todos.Items) != 2 || todos.Items[0].Content != "step one" || todos.Items[0].Status != TodoCompletedStatus {
		t.Errorf("got %+v", todos.Items)
	}
}

func TestBridge_WriteResult_BuildsNewFileDiff(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	details, _ := json.Marshal(map[string]any{"newFile": true, "patch": "--- a\n+++ b\n@@ -0,0 +1,1 @@\n+hello\n"})
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "write",
		ToolArgs:   map[string]any{"path": "new.txt"},
		ToolResult: &msg.ToolResultMessage{Content: msg.Blocks{msg.Text("ok")}, Details: details},
	}, ts, 4000)

	got := waitForOneSent(t, f)
	call, ok := got.(msgCommitToolCall)
	if !ok {
		t.Fatalf("sent = %T, want msgCommitToolCall", got)
	}
	if call.View.Diff == nil {
		t.Fatal("write result should carry a Diff")
	}
	if !call.View.Diff.NewFile {
		t.Error("Diff.NewFile should be true")
	}
}

// --- Esc-abort vs the aborted call's own EventToolEnd --------------------

// TestBridge_AbortThenToolEnd_CommitsOnlyOnce covers the ordering finding
// 2 actually reproduces: finishTurn's StatusAborted path calls
// InFlightTools() before the interrupted call's own "Command aborted"
// EventToolEnd has been processed. InFlightTools must mark the call so
// that later EventToolEnd is a no-op, not a second committed block.
func TestBridge_AbortThenToolEnd_CommitsOnlyOnce(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}
	b.ts = ts

	b.handleEvent(harness.Event{Type: harness.EventToolStart, ToolCallID: "t1", ToolName: "bash"}, ts, 4000)

	// Esc's abort path runs first: the call is still in flight.
	views := b.InFlightTools()
	if len(views) != 1 {
		t.Fatalf("InFlightTools = %d views, want 1", len(views))
	}

	// The call's own "Command aborted" EventToolEnd arrives afterward,
	// racing the abort.
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "sleep 5"},
		ToolResult: &msg.ToolResultMessage{Content: msg.Blocks{msg.Text("Command aborted")}, IsError: true},
	}, ts, 4000)

	time.Sleep(20 * time.Millisecond) // give the committer goroutine a chance to send anything, if it were going to
	printed, sent := f.snapshot()
	if len(printed)+len(sent) != 0 {
		t.Fatalf("EventToolEnd after InFlightTools committed the same id again: printed=%v sent=%v", printed, sent)
	}
	if len(ts.toolStarts) != 0 {
		t.Error("toolStarts entry for the aborted call was not cleaned up")
	}
}

// TestBridge_ToolEndThenAbort_NoDuplicateInFlight covers the normal
// ordering: the aborted call's own EventToolEnd is processed (and
// committed) before finishTurn ever calls InFlightTools. InFlightTools
// must not report a call that has already ended.
func TestBridge_ToolEndThenAbort_NoDuplicateInFlight(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}
	b.ts = ts

	b.handleEvent(harness.Event{Type: harness.EventToolStart, ToolCallID: "t1", ToolName: "bash"}, ts, 4000)
	b.handleEvent(harness.Event{
		Type: harness.EventToolEnd, ToolCallID: "t1", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "sleep 5"},
		ToolResult: &msg.ToolResultMessage{Content: msg.Blocks{msg.Text("Command aborted")}, IsError: true},
	}, ts, 4000)

	got := waitForOneSent(t, f)
	if _, ok := got.(msgCommitToolCall); !ok {
		t.Fatalf("sent = %T, want msgCommitToolCall", got)
	}

	if views := b.InFlightTools(); len(views) != 0 {
		t.Errorf("InFlightTools = %d views after EventToolEnd already committed, want 0", len(views))
	}
}

// --- Busy-line label verbs (finding 6) -------------------------------------

func TestBusyLabelForToolStart_Verbs(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"read", "read", map[string]any{"path": "src/math.js"}, "Reading math.js"},
		{"glob", "glob", map[string]any{"pattern": "*.go"}, "Reading *.go"},
		{"grep", "grep", map[string]any{"pattern": "TODO"}, "Reading TODO"},
		{"tool_search", "tool_search", map[string]any{"query": "select:Read"}, "Reading select:Read"},
		{"web_fetch", "web_fetch", map[string]any{"url": "https://example.com/page"}, "Reading https://example.com/page"},
		{"edit", "edit", map[string]any{"path": "src/math.js"}, "Editing math.js"},
		{"write", "write", map[string]any{"path": "new/deep/file.txt"}, "Writing file.txt"},
		{"todo_write", "todo_write", nil, "Planning"},
		{"mcp tool", "mcp__grafana__query_prometheus", nil, "Running Mcp__grafana__query_prometheus"},
		{"unmapped tool falls back to Running <Name>", "some_custom_tool", nil, "Running Some_custom_tool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := busyLabelForToolStart(&turnState{}, harness.Event{ToolName: c.tool, ToolArgs: c.args})
			if got != c.want {
				t.Errorf("busyLabelForToolStart(%q) = %q, want %q", c.tool, got, c.want)
			}
		})
	}
}

func TestBusyLabelForToolStart_Bash_TruncatesLongCommand(t *testing.T) {
	cmd := "find . -name '*.go' -exec grep -l TODO {} ; # a very long trailing comment"
	got := busyLabelForToolStart(&turnState{}, harness.Event{ToolName: "bash", ToolArgs: map[string]any{"command": cmd}})
	want := "Running " + string([]rune(cmd)[:30]) + "…"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBusyLabelForToolStart_Bash_ShortCommandNotTruncated(t *testing.T) {
	got := busyLabelForToolStart(&turnState{}, harness.Event{ToolName: "bash", ToolArgs: map[string]any{"command": "npm test"}})
	if got != "Running npm test" {
		t.Errorf("got %q, want %q", got, "Running npm test")
	}
}

// TestBusyLabelForToolStart_Task_CountsLiveSubagents covers "task" ->
// "Running a subagent" for the first, in-flight dispatch, then "Running N
// subagents" once a second concurrent dispatch starts before the first
// ends (turnState.tasksInFlight, incremented here and decremented by
// Wire's label subscription on that call's EventToolEnd).
func TestBusyLabelForToolStart_Task_CountsLiveSubagents(t *testing.T) {
	ts := &turnState{}
	if got := busyLabelForToolStart(ts, harness.Event{ToolName: "task"}); got != "Running a subagent" {
		t.Errorf("first task dispatch: got %q, want %q", got, "Running a subagent")
	}
	if got := busyLabelForToolStart(ts, harness.Event{ToolName: "task"}); got != "Running 2 subagents" {
		t.Errorf("second concurrent task dispatch: got %q, want %q", got, "Running 2 subagents")
	}
}
