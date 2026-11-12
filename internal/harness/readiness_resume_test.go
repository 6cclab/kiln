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
	"github.com/andrepato/harness/internal/session/jsonl"
	"github.com/andrepato/harness/internal/tool"
)

// probeReopen drops the original in-memory state and recovers through the
// JSONL reader, as a new kiln process would after a crash.
func probeReopen(t *testing.T, rig *testRig) *Lane {
	t.Helper()
	if err := rig.Storage.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err := jsonl.Open(rig.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	opts := rig.H.opts
	opts.Storage = storage
	h, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	rig.Storage = storage
	rig.H = h
	lane, err := h.Lane("main")
	if err != nil {
		t.Fatal(err)
	}
	return lane
}

func probeSeed(t *testing.T, lane *Lane, rig *testRig, state OpState, calls []msg.ToolCall, pending *msg.ToolResultMessage, committed *msg.ToolResultMessage) (string, string) {
	t.Helper()
	promptID, assistantID, operationID := lane.newID(), lane.newID(), lane.newID()
	state.Batch.AssistantEntryID = assistantID
	state.Batch.TurnID = assistantID
	assistant := msg.AssistantMessage{API: "anthropic-messages", Model: "faux-1", Provider: "faux", Role: msg.RoleAssistant, StopReason: msg.StopToolUse, Timestamp: 1}
	for _, c := range calls {
		assistant.Content = append(assistant.Content, c)
	}
	writes := []session.Write{
		session.EntryWrite{Entry: session.Entry{ID: promptID, Type: session.EntryMessage, Message: userMsg("do work")}},
		session.EntryWrite{Entry: session.Entry{ID: assistantID, ParentID: &promptID, Type: session.EntryMessage, Message: assistant}},
	}
	tip := assistantID
	if committed != nil {
		id := state.Batch.Calls[0].ResultEntryID
		writes = append(writes, session.EntryWrite{Entry: session.Entry{ID: id, ParentID: &assistantID, Type: session.EntryMessage, Message: *committed}})
		tip = id
	}
	tw, _ := session.SetValue(session.BranchTip("main"), &tip)
	ow, _ := session.SetValue(opStateAddr(operationID), state)
	lw, _ := session.SetValue(session.LaneStateValue("main"), session.LaneState{CurrentOperationID: &operationID})
	writes = append(writes, tw, ow, lw)
	if pending != nil {
		raw, _ := json.Marshal(PendingToolResultPayload{Payload: *pending})
		pw, _ := session.SetValue(session.PendingEntry(state.Batch.Calls[0].ResultEntryID), json.RawMessage(raw))
		writes = append(writes, pw)
	}
	if _, err := rig.Storage.Commit(writes); err != nil {
		t.Fatal(err)
	}
	return operationID, assistantID
}

func probeResults(t *testing.T, lane *Lane) int {
	t.Helper()
	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Message != nil && e.Message.MessageRole() == msg.RoleToolResult {
			n++
		}
	}
	return n
}

func TestReadinessResumeMaterializesOutcomeReady(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"bash"})
	lane := rig.mustLane("main")
	id := lane.newID()
	result := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "tc1", ToolName: "bash", Content: msg.Blocks{msg.Text("effect completed")}}
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect completed"})
	probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "outcome_ready"}}}}, []msg.ToolCall{call}, &result, nil)
	lane = probeReopen(t, rig)
	got, err := lane.Resume(context.Background())
	if err != nil {
		t.Fatalf("Resume: %v, status %s", err, got.Status)
	}
	if n := probeResults(t, lane); n != 1 {
		t.Fatalf("committed tool results = %d, want 1; pending result was discarded", n)
	}
}

func TestReadinessResumeMissingOrInvalidSavedResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "missing"},
		{name: "malformed", raw: json.RawMessage(`{"payload":[]}`)},
		{name: "invalid", raw: json.RawMessage(`{"payload":{"role":"toolResult","toolCallId":"wrong","toolName":"bash"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"bash"})
			lane := rig.mustLane("main")
			id := lane.newID()
			call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect"})
			probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "outcome_ready"}}}}, []msg.ToolCall{call}, nil, nil)
			if tc.raw != nil {
				write := session.SetValueRaw(session.NamespacePendingEntry, id, tc.raw)
				if _, err := rig.Storage.Commit([]session.Write{write}); err != nil {
					t.Fatal(err)
				}
			}
			lane = probeReopen(t, rig)
			got, err := lane.Resume(context.Background())
			if got.Status != StatusFailed || err == nil {
				t.Fatalf("Resume status=%s err=%v, want explicit failure", got.Status, err)
			}
			entries, err := lane.FindEntries(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			result, ok := entries[0].Message.(msg.ToolResultMessage)
			if !ok || !result.IsError || result.ToolCallID != call.ID || !strings.Contains(msg.TextOf(result.Content), "saved result") {
				t.Fatalf("missing explicit paired error: %#v", entries[0].Message)
			}
			if n := probeResults(t, lane); n != 1 {
				t.Fatalf("tool results=%d, want exactly one", n)
			}
			if len(rig.Faux.Requests()) != 0 {
				t.Fatal("model was asked to continue after uncertain result")
			}
		})
	}
}

func TestReadinessResumeAlreadyCommittedResultIsNotDuplicated(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"bash"})
	lane := rig.mustLane("main")
	id := lane.newID()
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect"})
	result := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: call.ID, ToolName: call.Name, Content: msg.Blocks{msg.Text("done")}}
	probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "outcome_ready"}}}}, []msg.ToolCall{call}, nil, &result)
	lane = probeReopen(t, rig)
	got, err := lane.Resume(context.Background())
	if err != nil || got.Status != StatusCompleted {
		t.Fatalf("Resume status=%s err=%v", got.Status, err)
	}
	if n := probeResults(t, lane); n != 1 {
		t.Fatalf("tool results=%d, want exactly one", n)
	}
}

func TestReadinessResumeConflictingCommittedResultFailsClosed(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"bash"})
	lane := rig.mustLane("main")
	id := lane.newID()
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect"})
	wrong := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "other", ToolName: "bash", Content: msg.Blocks{msg.Text("wrong")}}
	probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "outcome_ready"}}}}, []msg.ToolCall{call}, nil, &wrong)
	lane = probeReopen(t, rig)
	got, err := lane.Resume(context.Background())
	if got.Status != StatusFailed || err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Resume status=%s err=%v, want explicit conflict failure", got.Status, err)
	}
	if n := probeResults(t, lane); n != 1 {
		t.Fatalf("tool results=%d, want no duplicate entry", n)
	}
	if len(rig.Faux.Requests()) != 0 {
		t.Fatal("model was asked to continue with a conflicting result")
	}
}

func TestReadinessConflictBlocksLaterPromptUntilRewind(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: valid continuation\n", []string{"bash"})
	lane := rig.mustLane("main")
	id := lane.newID()
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect"})
	wrong := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "other", ToolName: "bash", Content: msg.Blocks{msg.Text("wrong")}}
	_, assistantID := probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "outcome_ready"}}}}, []msg.ToolCall{call}, nil, &wrong)
	lane = probeReopen(t, rig)
	if got, err := lane.Resume(context.Background()); got.Status != StatusFailed || err == nil {
		t.Fatalf("Resume status=%s err=%v, want conflict failure", got.Status, err)
	}
	lane = probeReopen(t, rig) // terminal failure and bad branch survive a process restart
	badTip, ok := lane.GetTipID()
	if !ok {
		t.Fatal("missing corrupt branch tip")
	}
	if got, err := lane.Prompt(context.Background(), "continue", nil); err == nil || !strings.Contains(err.Error(), "invalid tool history") || !strings.Contains(err.Error(), "rewind") {
		t.Fatalf("Prompt on corrupt branch status=%s err=%v, want repair-required failure", got.Status, err)
	}
	if tip, _ := lane.GetTipID(); tip != badTip {
		t.Fatalf("blocked Prompt changed branch tip from %s to %s", badTip, tip)
	}
	if n := len(rig.Faux.Requests()); n != 0 {
		t.Fatalf("blocked Prompt sent %d model requests, want zero", n)
	}
	assistant := rig.Storage.GetEntries([]string{assistantID})[assistantID]
	if assistant.ParentID == nil {
		t.Fatal("assistant has no valid parent for rewind")
	}
	if err := lane.NavigateTree(context.Background(), assistant.ParentID); err != nil {
		t.Fatalf("NavigateTree to valid ancestor: %v", err)
	}
	got, err := lane.Prompt(context.Background(), "continue from here", nil)
	if err != nil || got.Status != StatusCompleted {
		t.Fatalf("Prompt after rewind status=%s err=%v", got.Status, err)
	}
	if n := len(rig.Faux.Requests()); n != 1 {
		t.Fatalf("Prompt after rewind sent %d model requests, want one", n)
	}
}

func TestReadinessWrongToolNameBlocksLaterPrompt(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: should not run\n", []string{"bash"})
	lane := rig.mustLane("main")
	id := lane.newID()
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect"})
	wrong := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: call.ID, ToolName: "read", Content: msg.Blocks{msg.Text("wrong")}}
	probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "outcome_ready"}}}}, []msg.ToolCall{call}, nil, &wrong)
	lane = probeReopen(t, rig)
	if got, err := lane.Resume(context.Background()); got.Status != StatusFailed || err == nil {
		t.Fatalf("Resume status=%s err=%v, want conflict failure", got.Status, err)
	}
	lane = probeReopen(t, rig)
	if got, err := lane.Prompt(context.Background(), "continue", nil); err == nil || !strings.Contains(err.Error(), "invalid tool history") {
		t.Fatalf("Prompt on wrong-name branch status=%s err=%v", got.Status, err)
	}
	if n := len(rig.Faux.Requests()); n != 0 {
		t.Fatalf("Prompt sent %d model requests, want zero", n)
	}
}

func TestReadinessCorruptCheckpointNeverRequestsModel(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: should not run\n", []string{"bash"})
	lane := rig.mustLane("main")
	id := lane.newID()
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect"})
	wrong := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "other", ToolName: call.Name, Content: msg.Blocks{msg.Text("wrong")}}
	probeSeed(t, lane, rig, OpState{At: AtAssistantReady, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "outcome_ready"}}}}, []msg.ToolCall{call}, nil, &wrong)
	lane = probeReopen(t, rig)
	got, err := lane.Resume(context.Background())
	if got.Status != StatusFailed || err == nil || !strings.Contains(err.Error(), "invalid tool history") {
		t.Fatalf("Resume on corrupt checkpoint status=%s err=%v requests=%d, want integrity failure", got.Status, err, len(rig.Faux.Requests()))
	}
	if n := len(rig.Faux.Requests()); n != 0 {
		t.Fatalf("Resume sent %d model requests from corrupt branch", n)
	}
}

func TestReadinessConflictBlocksManualCompaction(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: summary\n", []string{"bash"})
	lane := rig.mustLane("main")
	id := lane.newID()
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect"})
	wrong := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "other", ToolName: call.Name, Content: msg.Blocks{msg.Text("wrong")}}
	probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "outcome_ready"}}}}, []msg.ToolCall{call}, nil, &wrong)
	lane = probeReopen(t, rig)
	if got, err := lane.Resume(context.Background()); got.Status != StatusFailed || err == nil {
		t.Fatalf("Resume status=%s err=%v, want conflict failure", got.Status, err)
	}
	// Give manual compaction a later turn to summarize so the baseline path
	// actually reaches its provider instead of returning at the cut point.
	userID, assistantID := lane.newID(), lane.newID()
	user := session.Entry{ID: userID, ParentID: &id, Type: session.EntryMessage, Message: userMsg("later")}
	assistant := session.Entry{ID: assistantID, ParentID: &userID, Type: session.EntryMessage,
		Message: msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("later reply")}}}
	tipWrite, _ := session.SetValue(session.BranchTip("main"), &assistantID)
	if _, err := rig.Storage.Commit([]session.Write{session.EntryWrite{Entry: user}, session.EntryWrite{Entry: assistant}, tipWrite}); err != nil {
		t.Fatal(err)
	}
	lane = probeReopen(t, rig)
	if err := lane.Compact(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "invalid tool history") {
		t.Fatalf("Compact on corrupt branch err=%v requests=%d, want integrity failure before a model call", err, len(rig.Faux.Requests()))
	}
	if n := len(rig.Faux.Requests()); n != 0 {
		t.Fatalf("Compact sent %d model requests, want zero", n)
	}
}

func TestReadinessValidToolHistoryAllowsLaterPromptAfterReopen(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: recovered\n  - text: later\n", []string{"bash"})
	lane := rig.mustLane("main")
	id := lane.newID()
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect"})
	result := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: call.ID, ToolName: call.Name, Content: msg.Blocks{msg.Text("done")}}
	probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "outcome_ready"}}}}, []msg.ToolCall{call}, nil, &result)
	lane = probeReopen(t, rig)
	if got, err := lane.Resume(context.Background()); err != nil || got.Status != StatusCompleted {
		t.Fatalf("Resume status=%s err=%v", got.Status, err)
	}
	lane = probeReopen(t, rig)
	if got, err := lane.Prompt(context.Background(), "new turn", nil); err != nil || got.Status != StatusCompleted {
		t.Fatalf("Prompt status=%s err=%v", got.Status, err)
	}
	if n := len(rig.Faux.Requests()); n != 2 {
		t.Fatalf("valid history made %d requests, want two", n)
	}
}

func TestReadinessResumeCompletesMultiToolBatch(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"bash"})
	lane := rig.mustLane("main")
	firstID, secondID := lane.newID(), lane.newID()
	first := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "tc1", ToolName: "bash", Content: msg.Blocks{msg.Text("first done")}}
	calls := []msg.ToolCall{msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo first"}), msg.NewToolCall("tc2", "bash", map[string]any{"command": "echo second"})}
	batch := []ToolCallState{{ResultEntryID: firstID, SourceIndex: 0, Status: "planned"}, {ResultEntryID: secondID, SourceIndex: 1, Status: "planned"}}
	opID, assistantID := probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: batch}}, calls, nil, nil)
	if err := lane.commitToolPending(opID, assistantID, calls[0], 0, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := lane.commitToolResult(context.Background(), opID, assistantID, assistantID, calls[0], 0, first, "", batch, false); err != nil {
		t.Fatal(err)
	}
	raw, _, ok := rig.Storage.GetValue(session.NamespaceOpState, opID)
	if !ok {
		t.Fatal("no op state")
	}
	var st OpState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.At != AtTools || st.Batch == nil || len(st.Batch.Calls) != 2 {
		t.Fatalf("real first-result state=%+v, want durable tools batch", st)
	}
	lane = probeReopen(t, rig)
	got, err := lane.Resume(context.Background())
	if err != nil {
		t.Fatalf("Resume: %v, status %s", err, got.Status)
	}
	if n := probeResults(t, lane); n != 2 {
		t.Fatalf("committed tool results = %d, want 2; second call was lost", n)
	}
}

// A recovered batch must finish every paired result before deciding whether
// worker artifacts can be integrated. The first effect was already durable;
// only the second, still-planned call may execute after reopening.
func TestReadinessRecoveredBatchCompletesBeforeIntegration(t *testing.T) {
	executions := 0
	rig := newTestRigWith(t, "model: faux-1\nsteps:\n  - text: resumed\n", []string{"effect"}, nil,
		func(opts *Options, _ *provider.Registry) {
			opts.Tools.Add(&tool.Tool{Name: "effect", Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
				Execute: func(context.Context, json.RawMessage, tool.Update, tool.Invocation) (tool.Result, error) {
					executions++
					return tool.Text("second ran"), nil
				}})
		})
	lane := rig.mustLane("main")
	firstID, secondID := lane.newID(), lane.newID()
	calls := []msg.ToolCall{msg.NewToolCall("tc1", "effect", map[string]any{}), msg.NewToolCall("tc2", "effect", map[string]any{})}
	batch := []ToolCallState{{ResultEntryID: firstID, SourceIndex: 0, Status: "planned"}, {ResultEntryID: secondID, SourceIndex: 1, Status: "planned"}}
	opID, assistantID := probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: batch}}, calls, nil, nil)
	first := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "tc1", ToolName: "effect", Content: msg.Blocks{msg.Text("first committed")}}
	if err := lane.commitToolPending(opID, assistantID, calls[0], 0, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := lane.commitToolResult(context.Background(), opID, assistantID, assistantID, calls[0], 0, first, "", batch, false); err != nil {
		t.Fatal(err)
	}
	lane = probeReopen(t, rig)
	completionCalls := 0
	rig.H.SetToolBatchComplete(func(_ context.Context, _ session.Storage, _, _ string, gotCalls []msg.ToolCall, results []msg.ToolResultMessage, recovered bool) (*ToolBatchCompletion, error) {
		completionCalls++
		if !recovered || len(gotCalls) != 2 || len(results) != 2 || results[0].ToolCallID != "tc1" || results[1].ToolCallID != "tc2" ||
			msg.TextOf(results[0].Content) != "first committed" || msg.TextOf(results[1].Content) != "second ran" || executions != 1 {
			t.Fatalf("incomplete recovered batch: recovered=%t calls=%d results=%+v executions=%d", recovered, len(gotCalls), results, executions)
		}
		return &ToolBatchCompletion{Status: "applied", Text: "Worker batch integrated after recovery."}, nil
	})
	got, err := lane.Resume(context.Background())
	if err != nil || got.Status != StatusCompleted {
		t.Fatalf("Resume status=%s err=%v", got.Status, err)
	}
	if completionCalls != 1 || executions != 1 || probeResults(t, lane) != 2 {
		t.Fatalf("completion calls=%d executions=%d results=%d", completionCalls, executions, probeResults(t, lane))
	}
	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, entry := range entries {
		if user, ok := entry.Message.(msg.UserMessage); ok && user.KilnHook == "WorkerIntegration" {
			notes++
		}
	}
	if notes != 1 || len(rig.Faux.Requests()) != 1 || !strings.Contains(string(rig.Faux.Requests()[0].Messages), "Worker batch integrated after recovery.") {
		t.Fatalf("durable notice count=%d requests=%+v", notes, rig.Faux.Requests())
	}
}

func TestReadinessResumeRepairsLegacyCheckpointGap(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"bash"})
	lane := rig.mustLane("main")
	firstID, secondID := lane.newID(), lane.newID()
	first := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "tc1", ToolName: "bash", Content: msg.Blocks{msg.Text("first done")}}
	calls := []msg.ToolCall{msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo first"}), msg.NewToolCall("tc2", "bash", map[string]any{"command": "echo second"})}
	batch := []ToolCallState{{ResultEntryID: firstID, SourceIndex: 0, Status: "planned"}, {ResultEntryID: secondID, SourceIndex: 1, Status: "planned"}}
	opID, assistantID := probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: batch}}, calls, nil, nil)
	if err := lane.commitToolPending(opID, assistantID, calls[0], 0, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := lane.commitToolResult(context.Background(), opID, assistantID, assistantID, calls[0], 0, first, "", batch, false); err != nil {
		t.Fatal(err)
	}
	legacy, _ := session.SetValue(opStateAddr(opID), OpState{At: AtCheckpoint, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), LatestAssistantEntryID: &assistantID})
	if _, err := rig.Storage.Commit([]session.Write{legacy}); err != nil {
		t.Fatal(err)
	}
	lane = probeReopen(t, rig)
	got, err := lane.Resume(context.Background())
	if err != nil || got.Status != StatusCompleted {
		t.Fatalf("Resume status=%s error=%v", got.Status, err)
	}
	if n := probeResults(t, lane); n != 2 {
		t.Fatalf("committed tool results = %d, want 2", n)
	}
}

func TestReadinessLegacyConcurrentGapDoesNotReplayUnknownEffect(t *testing.T) {
	executed := false
	rig := newTestRigWith(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"parallel"}, nil,
		func(opts *Options, _ *provider.Registry) {
			opts.Tools.Add(&tool.Tool{Name: "parallel", Concurrent: true, Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
				Execute: func(context.Context, json.RawMessage, tool.Update, tool.Invocation) (tool.Result, error) {
					executed = true
					return tool.Text("ran"), nil
				}})
		})
	lane := rig.mustLane("main")
	firstID, secondID := lane.newID(), lane.newID()
	first := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "tc1", ToolName: "parallel", Content: msg.Blocks{msg.Text("first done")}}
	calls := []msg.ToolCall{msg.NewToolCall("tc1", "parallel", map[string]any{}), msg.NewToolCall("tc2", "parallel", map[string]any{})}
	batch := []ToolCallState{{ResultEntryID: firstID, SourceIndex: 0, Status: "planned"}, {ResultEntryID: secondID, SourceIndex: 1, Status: "planned"}}
	opID, assistantID := probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: batch}}, calls, nil, nil)
	if err := lane.commitToolPending(opID, assistantID, calls[0], 0, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := lane.commitToolResult(context.Background(), opID, assistantID, assistantID, calls[0], 0, first, "", batch, false); err != nil {
		t.Fatal(err)
	}
	legacy, _ := session.SetValue(opStateAddr(opID), OpState{At: AtCheckpoint, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), LatestAssistantEntryID: &assistantID})
	if _, err := rig.Storage.Commit([]session.Write{legacy}); err != nil {
		t.Fatal(err)
	}
	lane = probeReopen(t, rig)
	got, err := lane.Resume(context.Background())
	if got.Status != StatusFailed || err == nil || executed {
		t.Fatalf("Resume status=%s err=%v executed=%v, want uncertain failure without replay", got.Status, err, executed)
	}
	if n := probeResults(t, lane); n != 2 {
		t.Fatalf("tool results=%d, want committed first and uncertain second", n)
	}
}

func TestReadinessResumeCancellationIsAborted(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"bash"})
	lane := rig.mustLane("main")
	id := lane.newID()
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "echo effect"})
	probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "planned"}}}}, []msg.ToolCall{call}, nil, nil)
	lane = probeReopen(t, rig)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := lane.Resume(ctx)
	if got.Status != StatusAborted {
		t.Fatalf("Resume status=%q error=%v; want aborted", got.Status, err)
	}
}

func TestReadinessResumeCancellationDuringToolIsAborted(t *testing.T) {
	started := make(chan struct{})
	rig := newTestRigWith(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"wait_cancel"}, nil,
		func(opts *Options, _ *provider.Registry) {
			opts.Tools.Add(&tool.Tool{Name: "wait_cancel", Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
				Execute: func(ctx context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
					close(started)
					<-ctx.Done()
					return tool.Result{}, ctx.Err()
				}})
		})
	lane := rig.mustLane("main")
	id := lane.newID()
	call := msg.NewToolCall("tc1", "wait_cancel", map[string]any{})
	probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "planned"}}}}, []msg.ToolCall{call}, nil, nil)
	lane = probeReopen(t, rig)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan RunResult, 1)
	go func() { result, _ := lane.Resume(ctx); done <- result }()
	<-started
	cancel()
	result := <-done
	if result.Status != StatusAborted {
		t.Fatalf("Resume status=%s, want aborted", result.Status)
	}
	if n := probeResults(t, lane); n != 1 {
		t.Fatalf("tool results=%d, want one paired canceled result", n)
	}
}

func TestReadinessResumeDoesNotDuplicateInFlightEffect(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"bash"})
	lane := rig.mustLane("main")
	file := filepath.Join(lane.h.opts.Cwd, "effect.txt")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	id := lane.newID()
	call := msg.NewToolCall("tc1", "bash", map[string]any{"command": "printf x >> effect.txt"})
	probeSeed(t, lane, rig, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Batch: &Batch{Calls: []ToolCallState{{ResultEntryID: id, SourceIndex: 0, Status: "effect_pending"}}}}, []msg.ToolCall{call}, nil, nil)
	lane = probeReopen(t, rig)
	got, err := lane.Resume(context.Background())
	if got.Status != StatusFailed || err == nil || !strings.Contains(err.Error(), "effect") {
		t.Fatalf("Resume: status=%s error=%v, want explicit uncertain failure", got.Status, err)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "x" {
		t.Fatalf("effect bytes=%q, want one x; command duplicated after crash", body)
	}
	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("branch has %d entries, want paired tool result", len(entries))
	}
	result, ok := entries[0].Message.(msg.ToolResultMessage)
	if !ok || result.ToolCallID != call.ID || !result.IsError || !strings.Contains(msg.TextOf(result.Content), "unknown") {
		t.Fatalf("uncertain tool call has no explicit error result: %#v", entries[0].Message)
	}
}
