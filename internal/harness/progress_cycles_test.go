package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

func failedCycleCall(label string) (msg.ToolCall, msg.ToolResultMessage) {
	return msg.NewToolCall(label, "read", map[string]any{"path": label + ".txt"}),
		msg.ToolResultMessage{IsError: true, Content: msg.Blocks{msg.Text("missing " + label)}}
}

func TestCycleGuardPatterns(t *testing.T) {
	for _, tc := range []struct {
		name, sequence string
		limit, stopAt  int
	}{
		{"ABAB", "ABAB", 2, 4},
		{"ABCABC", "ABCABC", 2, 6},
		{"three cycles", "ABABAB", 3, 6},
		{"changed sequence", "ABAC", 2, 0},
		{"single failure repeated", "AAAA", 2, 0},
		{"disabled", "ABAB", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := progressTracker{cycle: cycleGuard{limit: tc.limit}}
			for i, symbol := range tc.sequence {
				call, result := failedCycleCall(string(symbol))
				err := tracker.observe(call, result)
				if tc.stopAt == i+1 {
					if err == nil || !strings.Contains(err.Error(), "cycle") {
						t.Fatalf("step %d: want cycle stop, got %v", i+1, err)
					}
				} else if err != nil {
					t.Fatalf("step %d: unexpected stop: %v", i+1, err)
				}
			}
		})
	}
}

func TestCycleGuardSuccessAndChangedResultReset(t *testing.T) {
	for _, tc := range []struct {
		name    string
		symbols string
		resetAt int
	}{
		{"successful polling", "ABAB", 2},
		{"changed displayed result", "ABAB", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := progressTracker{cycle: cycleGuard{limit: 2}}
			for i, symbol := range tc.symbols {
				call, result := failedCycleCall(string(symbol))
				if i == tc.resetAt && tc.name == "successful polling" {
					result.IsError = false
				} else if i == tc.resetAt {
					result.Content = msg.Blocks{msg.Text("a different error")}
				}
				if err := tracker.observe(call, result); err != nil {
					t.Fatalf("step %d: %v", i+1, err)
				}
			}
		})
	}
}

func TestCycleGuardDoesNotStopSuccessfulPolling(t *testing.T) {
	tracker := progressTracker{cycle: cycleGuard{limit: 2}}
	for _, symbol := range "ABABABAB" {
		call, result := failedCycleCall(string(symbol))
		result.IsError = false
		if err := tracker.observe(call, result); err != nil {
			t.Fatal(err)
		}
	}
	if tracker.cycle.count != 0 {
		t.Fatalf("successful results retained in failure history: %d", tracker.cycle.count)
	}
}

func TestCycleGuardBoundedHistory(t *testing.T) {
	tracker := progressTracker{cycle: cycleGuard{limit: 10}}
	for i := 0; i < 200; i++ {
		call, result := failedCycleCall(string(rune('A' + i%5)))
		if err := tracker.observe(call, result); err != nil {
			t.Fatal(err)
		}
	}
	if tracker.cycle.count != 40 {
		t.Fatalf("retained %d fingerprints, want 40", tracker.cycle.count)
	}
}

func TestCycleGuardPairsSkippedCallsAndResetsPerPrompt(t *testing.T) {
	calls := []msg.ToolCall{
		msg.NewToolCall("a1", "read", map[string]any{"path": "a-missing"}),
		msg.NewToolCall("b1", "read", map[string]any{"path": "b-missing"}),
		msg.NewToolCall("a2", "read", map[string]any{"path": "a-missing"}),
		msg.NewToolCall("b2", "read", map[string]any{"path": "b-missing"}),
		msg.NewToolCall("write", "write", map[string]any{"path": "cycle-marker.txt", "content": "must not run"}),
	}
	turns := []func() *msg.AssistantMessage{
		func() *msg.AssistantMessage { return assistantToolCalls(calls...) },
		func() *msg.AssistantMessage {
			return assistantToolCalls(
				msg.NewToolCall("a-next", "read", map[string]any{"path": "a-missing"}),
				msg.NewToolCall("b-next", "read", map[string]any{"path": "b-missing"}),
			)
		},
		func() *msg.AssistantMessage { return assistantText("recovered") },
	}
	h, lane := newConcurrentTestRig(t, turns)
	h.opts.CycleLimit = 2
	first, err := lane.Prompt(context.Background(), "try", nil)
	if err == nil || first.Status != StatusFailed || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("first prompt = %#v, %v", first, err)
	}
	if _, err := os.Stat(filepath.Join(h.opts.Cwd, "cycle-marker.txt")); !os.IsNotExist(err) {
		t.Fatalf("later write executed: %v", err)
	}
	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]msg.ToolResultMessage{}
	for _, entry := range entries {
		if entry.Type == session.EntryMessage {
			if result, ok := entry.Message.(msg.ToolResultMessage); ok {
				results[result.ToolCallID] = result
			}
		}
	}
	if len(results) != len(calls) || !results["write"].IsError || results["write"].Usage != nil {
		t.Fatalf("incomplete or successful skipped result: %+v", results)
	}
	second, err := lane.Prompt(context.Background(), "try again", nil)
	if err != nil || second.Status != StatusCompleted {
		t.Fatalf("second prompt = %#v, %v", second, err)
	}
}
