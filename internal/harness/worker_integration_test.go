package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// A crash after the last tool result leaves the complete batch at tools.
// Recovery makes one explicit decision and persists its note before asking
// the model again; the tool effect is never replayed.
func TestWorkerIntegrationResumeAfterResultBeforeDecision(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply bool
	}{
		{"before apply", false},
		{"after apply before report", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t, `model: faux-1
steps:
  - tool_call: {name: read, args: {path: note.txt}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "model omitted integration status"
`, []string{"read"})
			if err := os.WriteFile(filepath.Join(rig.H.opts.Cwd, "note.txt"), []byte("note\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			lane := rig.mustLane("main")
			marker := filepath.Join(rig.H.opts.Cwd, "applied.txt")
			calls := 0
			rig.H.SetToolBatchComplete(func(_ context.Context, storage session.Storage, _, assistantID string, _ []msg.ToolCall, _ []msg.ToolResultMessage, recovered bool) (*ToolBatchCompletion, error) {
				calls++
				if recovered {
					t.Fatal("first process received recovered callback")
				}
				raw, _ := json.Marshal(ToolBatchCompletion{Status: "intent", Artifacts: []string{"changes.patch"}})
				if _, err := storage.Commit([]session.Write{session.SetValueRaw(WorkerIntegrationNamespace, assistantID, raw)}); err != nil {
					return nil, err
				}
				if tc.apply {
					if err := os.WriteFile(marker, []byte("applied\n"), 0o600); err != nil {
						return nil, err
					}
				}
				return nil, context.Canceled // simulated process stop before outcome commit
			})
			if _, err := lane.Prompt(context.Background(), "read note", nil); err == nil {
				t.Fatal("fault injection did not stop after durable tool result")
			}
			if calls != 1 {
				t.Fatalf("completion calls=%d", calls)
			}
			if _, pending := lane.PendingOperation(); !pending {
				t.Fatal("integration fault cleared pending operation")
			}
			lane = probeReopen(t, rig)
			var notice Event
			rig.H.Events().On(EventWorkerIntegration, func(ev Event) { notice = ev })
			rig.H.SetToolBatchComplete(func(_ context.Context, storage session.Storage, _, assistantID string, _ []msg.ToolCall, _ []msg.ToolResultMessage, recovered bool) (*ToolBatchCompletion, error) {
				if !recovered {
					t.Fatal("reopened process did not mark recovery")
				}
				calls++
				raw, _, ok := storage.GetValue(WorkerIntegrationNamespace, assistantID)
				if !ok || !strings.Contains(string(raw), `"intent"`) {
					t.Fatalf("intent missing after reopen: %s", raw)
				}
				return &ToolBatchCompletion{Status: "uncertain", Text: "Worker patch may have applied; inspect changes.patch", Artifacts: []string{"changes.patch"}}, nil
			})
			if _, err := lane.Resume(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("recovery completion calls=%d", calls)
			}
			if notice.IntegrationStatus != "uncertain" || !strings.Contains(notice.IntegrationText, "changes.patch") {
				t.Fatalf("visible notice=%+v", notice)
			}
			entries, err := lane.FindEntries(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var durable bool
			for _, entry := range entries {
				if user, ok := entry.Message.(msg.UserMessage); ok && user.KilnHook == "WorkerIntegration" && strings.Contains(msg.TextOf(user.Content), "changes.patch") {
					durable = true
				}
			}
			if !durable {
				t.Fatal("recovered integration decision not in branch history")
			}
			_, err = os.Stat(marker)
			if tc.apply && err != nil {
				t.Fatal("applied effect disappeared")
			}
			if !tc.apply && !os.IsNotExist(err) {
				t.Fatal("recovery replayed external effect")
			}
		})
	}
}

func TestWorkerIntegrationResumeAfterDurableOutcomeBeforeEvent(t *testing.T) {
	for _, at := range []OpAt{AtCheckpoint, AtAssistantReady, AtAssistantEffectPending} {
		t.Run(string(at), func(t *testing.T) {
			rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: done\n", []string{"read"})
			lane := rig.mustLane("main")
			promptID, assistantID, resultID, noticeID, operationID := lane.newID(), lane.newID(), lane.newID(), lane.newID(), lane.newID()
			call := msg.NewToolCall("tc1", "read", map[string]any{"path": "note.txt"})
			prompt := session.Entry{ID: promptID, Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("read")}}}
			assistant := session.Entry{ID: assistantID, ParentID: &promptID, Type: session.EntryMessage, Message: msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{call}, StopReason: msg.StopToolUse}}
			result := session.Entry{ID: resultID, ParentID: &assistantID, Type: session.EntryMessage, Message: msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "tc1", ToolName: "read", Content: msg.Blocks{msg.Text("done")}}}
			text := "Worker batch integrated one patch: one.txt."
			notice := session.Entry{ID: noticeID, ParentID: &resultID, Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, KilnHook: "WorkerIntegration", Content: msg.Blocks{msg.Text(text)}}}
			stateW, _ := session.SetValue(opStateAddr(operationID), OpState{At: at, Control: OpControl{Status: "running"}, Settings: lane.opSettings(), Continuation: &Continuation{Kind: "need_assistant"}, LatestAssistantEntryID: &assistantID})
			laneW, _ := session.SetValue(session.LaneStateValue("main"), session.LaneState{CurrentOperationID: &operationID})
			tipW, _ := session.SetValue(session.BranchTip("main"), &noticeID)
			raw, _ := json.Marshal(ToolBatchCompletion{Status: "applied", Text: text})
			if _, err := rig.Storage.Commit([]session.Write{session.EntryWrite{Entry: prompt}, session.EntryWrite{Entry: assistant}, session.EntryWrite{Entry: result}, session.EntryWrite{Entry: notice}, stateW, laneW, tipW, session.SetValueRaw(WorkerIntegrationNamespace, assistantID, raw)}); err != nil {
				t.Fatal(err)
			}
			lane = probeReopen(t, rig)
			var replayed []Event
			rig.H.Events().On(EventWorkerIntegration, func(ev Event) { replayed = append(replayed, ev) })
			if _, err := lane.Resume(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(replayed) != 1 || !replayed[0].IntegrationReplayed || replayed[0].IntegrationText != text {
				t.Fatalf("replayed events=%+v", replayed)
			}
			entries, err := lane.FindEntries(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, entry := range entries {
				if m, ok := entry.Message.(msg.UserMessage); ok && m.KilnHook == "WorkerIntegration" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("durable notices=%d, want one", count)
			}
		})
	}
}

func TestWorkerIntegrationCarriesOutcomeIntoNextRequestState(t *testing.T) {
	rig := newTestRig(t, `model: faux-1
steps:
  - tool_call: {name: read, args: {path: note.txt}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: done
`, []string{"read"})
	if err := os.WriteFile(filepath.Join(rig.H.opts.Cwd, "note.txt"), []byte("note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rig.H.SetToolBatchComplete(func(context.Context, session.Storage, string, string, []msg.ToolCall, []msg.ToolResultMessage, bool) (*ToolBatchCompletion, error) {
		return &ToolBatchCompletion{Status: "applied", Text: "Worker batch integrated one patch."}, nil
	})
	starts := 0
	rig.H.Events().On(EventMessageStart, func(ev Event) {
		starts++
		if starts != 2 {
			return
		}
		raw, _, ok := rig.Storage.GetValue(session.NamespaceOpState, ev.OperationID)
		if !ok {
			t.Fatal("second request has no durable operation state")
		}
		var state OpState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if state.At != AtAssistantEffectPending || state.LatestAssistantEntryID == nil {
			t.Fatalf("integration ID lost before next request: %+v", state)
		}
		if raw, _, ok := rig.Storage.GetValue(WorkerIntegrationNamespace, *state.LatestAssistantEntryID); !ok || !strings.Contains(string(raw), `"applied"`) {
			t.Fatalf("previous outcome not addressable during next request: %s", raw)
		}
	})
	if _, err := rig.mustLane("main").Prompt(context.Background(), "read note", nil); err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatalf("message starts=%d, want two", starts)
	}
}
