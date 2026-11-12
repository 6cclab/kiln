package harness

import (
	"fmt"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// validateBranchToolPairs checks the stored lineage, not a process-local
// recovery flag. A failed resume may clear its operation while leaving a
// conflicting tool result in the branch; that branch must never reach a
// provider request. Navigation to an earlier valid ancestor or a new
// session is the safe repair path.
func (l *Lane) validateBranchToolPairs(tip string) error {
	if tip == "" {
		return nil
	}
	entries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "oldestFirst"})
	if err != nil {
		return err
	}
	pending := make(map[string]string)
	var pendingOrder []string
	firstPending := func() string {
		for _, id := range pendingOrder {
			if _, exists := pending[id]; exists {
				return id
			}
		}
		return ""
	}
	for _, entry := range entries {
		switch message := entry.Message.(type) {
		case msg.AssistantMessage:
			if len(pending) != 0 {
				return l.invalidToolBranch("assistant reply before result for tool call %q", firstPending())
			}
			pendingOrder = nil
			for _, call := range msg.ToolCallsOf(message.Content) {
				if call.ID == "" {
					return l.invalidToolBranch("assistant entry %q has a tool call without an ID", entry.ID)
				}
				if _, exists := pending[call.ID]; exists {
					return l.invalidToolBranch("assistant entry %q repeats tool call ID %q", entry.ID, call.ID)
				}
				pendingOrder = append(pendingOrder, call.ID)
				pending[call.ID] = call.Name
			}
		case msg.ToolResultMessage:
			name, exists := pending[message.ToolCallID]
			if !exists {
				return l.invalidToolBranch("tool result %q has no matching assistant call", message.ToolCallID)
			}
			if name != message.ToolName {
				return l.invalidToolBranch("tool result %q names %q instead of %q", message.ToolCallID, message.ToolName, name)
			}
			delete(pending, message.ToolCallID)
		case msg.UserMessage:
			if len(pending) != 0 {
				return l.invalidToolBranch("user message before result for tool call %q", firstPending())
			}
		}
	}
	if len(pending) != 0 {
		return l.invalidToolBranch("missing result for tool call %q", firstPending())
	}
	return nil
}

func (l *Lane) invalidToolBranch(format string, args ...any) error {
	return fmt.Errorf("harness: lane %q: invalid tool history: %s; rewind to a valid point or start a new session", l.name, fmt.Sprintf(format, args...))
}
