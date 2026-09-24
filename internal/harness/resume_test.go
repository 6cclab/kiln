package harness

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// TestResumeFromToolsState simulates a crash between tool_start and the
// tool's result being committed: it hand-writes a lane/operation state at
// `at: "tools"` (as if a prior process had gotten exactly that far and
// died), reopens the storage into a fresh Harness/Lane, and asserts Resume
// completes the run — re-running the pending tool call and reaching a
// normal "completed" result.
func TestResumeFromToolsState(t *testing.T) {
	script := `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`
	rig := newTestRig(t, script, []string{"bash"})
	lane := rig.mustLane("main")

	// Hand-build the state a real crash would leave: a user prompt entry,
	// an assistant entry with one toolCall, and pi.lane.state/pi.op.state
	// pointing at "tools" with that call still "planned" (never executed).
	promptID := lane.newID()
	assistantID := lane.newID()
	resultID := lane.newID()
	operationID := lane.newID()

	toolCall := session.EntryWrite{Entry: session.Entry{
		ID:      promptID,
		Type:    session.EntryMessage,
		Message: userMsg("simulate a crash"),
	}}
	tipW1, _ := session.SetValue(session.BranchTip("main"), &promptID)
	if _, err := rig.Storage.Commit([]session.Write{toolCall, tipW1}); err != nil {
		t.Fatalf("seed prompt entry: %v", err)
	}

	assistantEntry := session.EntryWrite{Entry: session.Entry{
		ID:       assistantID,
		ParentID: &promptID,
		Type:     session.EntryMessage,
		Message:  assistantWithToolCall("tc1", "bash", map[string]any{"command": "echo hi"}),
	}}
	tipW2, _ := session.SetValue(session.BranchTip("main"), &assistantID)
	metaW, _ := session.SetValue(opMetaAddr(operationID), OpMeta{Intent: OpIntent{Kind: "run", PromptEntryIDs: []string{promptID}}, Lane: "main", OperationID: operationID, StartedAt: 1})
	stateW, _ := session.SetValue(opStateAddr(operationID), OpState{
		At: AtTools, Control: OpControl{Status: "running"}, Settings: lane.opSettings(),
		Batch: &Batch{AssistantEntryID: assistantID, TurnID: assistantID, Calls: []ToolCallState{
			{ResultEntryID: resultID, SourceIndex: 0, Status: "planned"},
		}},
	})
	laneState := session.LaneState{CurrentOperationID: &operationID, Inbox: []session.InboxItem{}}
	laneW, _ := session.SetValue(session.LaneStateValue("main"), laneState)

	if _, err := rig.Storage.Commit([]session.Write{assistantEntry, tipW2, metaW, stateW, laneW}); err != nil {
		t.Fatalf("seed crashed-mid-tools state: %v", err)
	}

	result, err := lane.Resume(context.Background())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("Resume status = %q, want completed (err=%v)", result.Status, result.Error)
	}

	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sawToolResult := false
	for _, e := range entries {
		if e.Type == session.EntryMessage && e.Message != nil && e.Message.MessageRole() == "toolResult" {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatal("Resume did not commit a toolResult entry for the pending tool call")
	}

	st, err := lane.laneState()
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentOperationID != nil {
		t.Fatalf("lane still has a current operation after Resume completed: %v", *st.CurrentOperationID)
	}
}

func userMsg(text string) msg.Message {
	return msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(text)}, Timestamp: 1}
}

func assistantWithToolCall(id, name string, args map[string]any) msg.Message {
	return msg.AssistantMessage{
		API: "anthropic-messages", Model: "faux-1", Provider: "faux",
		Role: msg.RoleAssistant, StopReason: msg.StopToolUse, Timestamp: 1,
		Content: msg.Blocks{msg.NewToolCall(id, name, args)},
	}
}
