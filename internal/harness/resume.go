package harness

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// Resume reopens a lane whose pi.lane.state carries a currentOperationId
// (typically because the previous process crashed mid-operation) and
// continues it. It replays the durably-recorded `at` state and, per the
// phase brief's minimum bar:
//   - at "tools": re-runs whichever tool calls in the batch never reached
//     "outcome_ready".
//   - at "checkpoint", "assistant.ready" or "assistant.effect_pending":
//     any partially streamed response is discarded (its pending frames are
//     never replayed into the transcript) and the assistant request is
//     issued again from the last committed tip.
//   - at "starting": treated the same as "checkpoint" (nothing but the
//     prompt entry itself was durably recorded yet).
//   - at any of the not-implemented states (deferred.*, summary.*): Resume
//     returns an error naming the state instead of guessing.
//
// PendingOperation reports whether this lane's durable state still carries
// an operation a previous process left running — normally because that
// process crashed or was killed before the operation reached a terminal
// state. finishOperation (turn.go) always clears
// pi.lane.state.currentOperationId on completion, abort or failure, so a
// non-nil value here can only mean the operation never got there: exactly
// the case Resume (above) knows how to continue. ok is false for a lane
// with no recorded state yet, or one whose last operation finished
// cleanly — both are "nothing to resume", not an error.
func (l *Lane) PendingOperation() (operationID string, ok bool) {
	st, err := l.laneState()
	if err != nil {
		return "", false
	}
	if st.CurrentOperationID == nil {
		return "", false
	}
	return *st.CurrentOperationID, true
}

func (l *Lane) Resume(ctx context.Context) (RunResult, error) {
	st, err := l.laneState()
	if err != nil {
		return RunResult{}, err
	}
	if st.CurrentOperationID == nil {
		return RunResult{}, fmt.Errorf("harness: lane %q has no operation to resume", l.name)
	}
	operationID := *st.CurrentOperationID

	raw, _, ok := l.h.opts.Storage.GetValue(session.NamespaceOpState, operationID)
	if !ok {
		return RunResult{}, fmt.Errorf("harness: lane %q: operation %s has no recorded state", l.name, operationID)
	}
	var opState OpState
	if err := json.Unmarshal(raw, &opState); err != nil {
		return RunResult{}, err
	}
	if notImplementedStates[opState.At] {
		return RunResult{}, fmt.Errorf("harness: resume from state %q is not implemented", opState.At)
	}

	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		return RunResult{}, fmt.Errorf("harness: lane %q already has a running operation", l.name)
	}
	l.running = true
	runCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.running = false
		l.cancel = nil
		l.mu.Unlock()
		cancel()
	}()

	tip, _ := l.GetTipID()

	l.h.events.Emit(Event{Type: EventRunResume, Lane: l.name, OperationID: operationID})

	switch opState.At {
	case AtTools:
		if opState.Batch != nil {
			for i, call := range opState.Batch.Calls {
				if call.Status == "outcome_ready" {
					continue
				}
				// The original ToolCall content (name/arguments) is not
				// recoverable from OpState alone in this phase (it lived in
				// the assistant message content, which is already
				// committed); re-derive it from the committed assistant
				// entry.
				entries := l.h.opts.Storage.GetEntries([]string{opState.Batch.AssistantEntryID})
				entry, ok := entries[opState.Batch.AssistantEntryID]
				if !ok {
					return RunResult{}, fmt.Errorf("harness: resume: missing assistant entry %s", opState.Batch.AssistantEntryID)
				}
				am, ok := entry.Message.(msg.AssistantMessage)
				if !ok {
					return RunResult{}, fmt.Errorf("harness: resume: entry %s is not an assistant message", opState.Batch.AssistantEntryID)
				}
				toolCalls := msg.ToolCallsOf(am.Content)
				if i >= len(toolCalls) {
					continue
				}
				var runErr error
				tip, runErr = l.executeOneTool(runCtx, operationID, tip, opState.Batch.AssistantEntryID, toolCalls[i], call.SourceIndex, opState.Batch.Calls)
				if runErr != nil {
					return l.finishFailed(operationID, tip, runErr), runErr
				}
			}
		}
		result := l.drive(runCtx, operationID, tip)
		return result, result.Error
	default:
		// starting / checkpoint / assistant.ready / assistant.effect_pending:
		// re-enter the loop from the last committed tip; any partial
		// streaming state is discarded.
		result := l.drive(runCtx, operationID, tip)
		return result, result.Error
	}
}
