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
// continues it. It replays the durably-recorded `at` state:
//   - at "tools": materializes saved outcomes, runs calls that were never
//     started, and reports an uncertain effect without re-running it.
//   - at "checkpoint": repairs older sessions that lost their remaining
//     tool batch after committing an intermediate result.
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
//
// Defensive check: finishOperation's clear and its pi.result write are one
// Commit call, so they can only ever land or fail together — a Commit
// failure leaves both unset, which is exactly the "never got there" case
// above, not a divergence. But if that ever changed (a future refactor
// splitting the write, or the clear being lost some other way), replaying
// an operation that already has a recorded result would redo a turn the
// user already saw finish. So PendingOperation double-checks: a result
// already on disk for this operation means it truly finished, regardless
// of what pi.lane.state still says.
func (l *Lane) PendingOperation() (operationID string, ok bool) {
	st, err := l.laneState()
	if err != nil {
		return "", false
	}
	if st.CurrentOperationID == nil {
		return "", false
	}
	id := *st.CurrentOperationID
	if _, _, found := l.h.opts.Storage.GetValue(session.NamespaceResult, id); found {
		return "", false
	}
	return id, true
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
	if opState.LatestAssistantEntryID != nil {
		l.replayWorkerIntegrationOutcome(operationID, *opState.LatestAssistantEntryID)
	}

	switch opState.At {
	case AtTools:
		return l.resumeToolBatch(runCtx, operationID, tip, opState.Batch)
	case AtCheckpoint:
		// Older sessions wrote checkpoint after *each* result in a batch,
		// dropping its remaining calls. Rebuild those calls from the last
		// assistant entry and the results on this branch before asking the
		// model again. A concurrent call may already have run; it remains
		// uncertain rather than being replayed.
		batch, err := l.recoverCheckpointBatch(operationID, tip, opState)
		if err != nil {
			result := l.finishFailed(operationID, tip, err)
			return result, result.Error
		}
		if batch != nil {
			return l.resumeToolBatch(runCtx, operationID, tip, batch)
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

func (l *Lane) replayWorkerIntegrationOutcome(operationID, assistantID string) {
	raw, _, ok := l.h.opts.Storage.GetValue(WorkerIntegrationNamespace, assistantID)
	if !ok {
		return
	}
	var outcome ToolBatchCompletion
	if json.Unmarshal(raw, &outcome) != nil || outcome.Status == "" || outcome.Status == "intent" || outcome.Text == "" {
		return
	}
	l.h.events.Emit(Event{Type: EventWorkerIntegration, Lane: l.name, OperationID: operationID, IntegrationStatus: outcome.Status, IntegrationText: outcome.Text, IntegrationReplayed: true})
}

func (l *Lane) resumeToolBatch(ctx context.Context, operationID, tip string, batch *Batch) (RunResult, error) {
	if batch == nil {
		err := fmt.Errorf("harness: resume: tools state has no batch")
		result := l.finishFailed(operationID, tip, err)
		return result, result.Error
	}
	entries := l.h.opts.Storage.GetEntries([]string{batch.AssistantEntryID})
	entry, ok := entries[batch.AssistantEntryID]
	if !ok {
		err := fmt.Errorf("harness: resume: missing assistant entry %s", batch.AssistantEntryID)
		result := l.finishFailed(operationID, tip, err)
		return result, result.Error
	}
	am, ok := entry.Message.(msg.AssistantMessage)
	if !ok {
		err := fmt.Errorf("harness: resume: entry %s is not an assistant message", batch.AssistantEntryID)
		result := l.finishFailed(operationID, tip, err)
		return result, result.Error
	}
	toolCalls := msg.ToolCallsOf(am.Content)
	if len(toolCalls) != len(batch.Calls) {
		err := fmt.Errorf("harness: resume: batch has %d calls, assistant has %d", len(batch.Calls), len(toolCalls))
		result := l.finishFailed(operationID, tip, err)
		return result, result.Error
	}
	uncertain := false
	for _, state := range batch.Calls {
		if state.Status == "effect_pending" {
			uncertain = true
		}
	}
	aborted := ctx.Err() != nil
	for i, state := range batch.Calls {
		call := toolCalls[i]
		if state.SourceIndex != i || state.ResultEntryID == "" {
			err := fmt.Errorf("harness: resume: invalid batch call at index %d", i)
			result := l.finishFailed(operationID, tip, err)
			return result, result.Error
		}
		if ctx.Err() != nil {
			aborted = true
		}
		var err error
		switch state.Status {
		case "outcome_ready":
			var missing bool
			tip, missing, err = l.materializeReadyResult(ctx, operationID, tip, batch, call, i)
			if missing {
				uncertain = true
			}
		case "effect_pending":
			result := resumeErrorResult(l.now(), call, "The previous process stopped while this tool call was running. Its effect is unknown; kiln did not run it again. Inspect the result before retrying.")
			tip, err = l.commitToolResult(ctx, operationID, tip, batch.AssistantEntryID, call, i, result, "", batch.Calls, false)
		case "planned":
			if uncertain || aborted {
				reason := "The tool call was not run because an earlier call's effect is unknown. Inspect that effect before retrying."
				if aborted && !uncertain {
					reason = "The tool call was not run because recovery was canceled."
				}
				result := resumeErrorResult(l.now(), call, reason)
				tip, err = l.commitToolResult(ctx, operationID, tip, batch.AssistantEntryID, call, i, result, "", batch.Calls, false)
			} else {
				tip, _, err = l.executeOneTool(ctx, operationID, tip, batch.AssistantEntryID, call, i, batch.Calls, nil)
				if err != nil && ctx.Err() != nil {
					aborted = true
					result := resumeErrorResult(l.now(), call, "Recovery was canceled while this tool call was starting. Its effect is unknown; kiln did not run it again.")
					tip, err = l.commitToolResult(ctx, operationID, tip, batch.AssistantEntryID, call, i, result, "", batch.Calls, false)
				}
			}
		default:
			err = fmt.Errorf("harness: resume: invalid tool status %q", state.Status)
		}
		if err != nil {
			result := l.finishFailed(operationID, tip, err)
			return result, result.Error
		}
	}
	if l.h.batchComplete() != nil {
		var completeErr error
		tip, completeErr = l.completeToolBatch(ctx, operationID, tip, batch.AssistantEntryID, toolCalls, batch.Calls, true)
		if completeErr != nil {
			// Leave the durable batch pending after a completion failure.
			// Recovery may need to report an uncertain external apply.
			result := RunResult{Status: StatusFailed, TipID: tip, Error: completeErr}
			return result, completeErr
		}
	}
	if aborted {
		result := l.finishAborted(operationID, tip)
		return result, result.Error
	}
	if uncertain {
		err := fmt.Errorf("harness: resume: one or more tool effects could not be confirmed; inspect them before retrying")
		result := l.finishFailed(operationID, tip, err)
		return result, result.Error
	}
	result := l.drive(ctx, operationID, tip)
	return result, result.Error
}

func resumeErrorResult(now int64, call msg.ToolCall, reason string) msg.ToolResultMessage {
	return msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: call.ID, ToolName: call.Name,
		Content: msg.Blocks{msg.Text(reason)}, IsError: true, Timestamp: now}
}

// materializeReadyResult commits the payload a finished tool left in
// pi.pending.entry. A missing or malformed payload becomes an explicit error
// result; it never causes the external tool to run again.
func (l *Lane) materializeReadyResult(ctx context.Context, operationID, tip string, batch *Batch, call msg.ToolCall, index int) (string, bool, error) {
	id := batch.Calls[index].ResultEntryID
	if entry, exists := l.h.opts.Storage.GetEntries([]string{id})[id]; exists {
		result, ok := entry.Message.(msg.ToolResultMessage)
		if !ok || result.ToolCallID != call.ID || result.ToolName != call.Name {
			return tip, false, fmt.Errorf("harness: resume: committed result %s does not match tool call %s", id, call.ID)
		}
		branch, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "newestFirst"})
		if err != nil {
			return tip, false, err
		}
		for _, ancestor := range branch {
			if ancestor.ID == id {
				return tip, false, nil
			}
		}
		return tip, false, fmt.Errorf("harness: resume: committed result %s is not on the current branch", id)
	}
	raw, _, exists := l.h.opts.Storage.GetValue(session.NamespacePendingEntry, id)
	var pending PendingToolResultPayload
	if exists && json.Unmarshal(raw, &pending) == nil && pending.Payload.Role == msg.RoleToolResult &&
		pending.Payload.ToolCallID == call.ID && pending.Payload.ToolName == call.Name {
		next, err := l.commitReadyToolResult(ctx, operationID, tip, batch.AssistantEntryID, call, index, pending.Payload, "", batch.Calls, true)
		return next, false, err
	}
	result := resumeErrorResult(l.now(), call, "The previous tool finished, but its saved result is missing or invalid. Its effect is unknown; kiln did not run it again. Inspect the result before retrying.")
	next, err := l.commitToolResult(ctx, operationID, tip, batch.AssistantEntryID, call, index, result, "", batch.Calls, false)
	return next, true, err
}

// recoverCheckpointBatch repairs sessions written before intermediate tool
// results kept their batch in op.state. Only calls known not to have started
// are replayed. A missing concurrent call beside an already committed result
// may have executed before the crash and is treated as uncertain.
func (l *Lane) recoverCheckpointBatch(operationID, tip string, state OpState) (*Batch, error) {
	if state.LatestAssistantEntryID == nil {
		return nil, nil
	}
	assistantID := *state.LatestAssistantEntryID
	entry, ok := l.h.opts.Storage.GetEntries([]string{assistantID})[assistantID]
	if !ok {
		return nil, fmt.Errorf("harness: resume: missing checkpoint assistant %s", assistantID)
	}
	am, ok := entry.Message.(msg.AssistantMessage)
	if !ok {
		return nil, fmt.Errorf("harness: resume: checkpoint entry %s is not an assistant message", assistantID)
	}
	calls := msg.ToolCallsOf(am.Content)
	if len(calls) == 0 {
		return nil, nil
	}
	branch, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "newestFirst", StopAtID: assistantID})
	if err != nil {
		return nil, err
	}
	if len(branch) == 0 || branch[len(branch)-1].ID != assistantID {
		return nil, fmt.Errorf("harness: resume: checkpoint assistant %s is not on the current branch", assistantID)
	}
	results := map[string]session.Entry{}
	for _, candidate := range branch[:len(branch)-1] {
		if result, ok := candidate.Message.(msg.ToolResultMessage); ok {
			if _, duplicate := results[result.ToolCallID]; duplicate {
				return nil, fmt.Errorf("harness: resume: duplicate result for tool call %s", result.ToolCallID)
			}
			results[result.ToolCallID] = candidate
		}
	}
	batch := &Batch{AssistantEntryID: assistantID, TurnID: assistantID, Calls: make([]ToolCallState, len(calls))}
	missing := false
	missingBeforeCommitted := false
	for i, call := range calls {
		if result, ok := results[call.ID]; ok {
			if missingBeforeCommitted {
				return nil, fmt.Errorf("harness: resume: checkpoint results are not in tool-call order")
			}
			body, _ := result.Message.(msg.ToolResultMessage)
			if body.ToolName != call.Name {
				return nil, fmt.Errorf("harness: resume: checkpoint result does not match tool call %s", call.ID)
			}
			batch.Calls[i] = ToolCallState{ResultEntryID: result.ID, SourceIndex: i, Status: "outcome_ready"}
			continue
		}
		missing = true
		missingBeforeCommitted = true
		status := "planned"
		// Only the built-in file/shell tools are known to have been
		// sequential in the old runtime. A plugin or task tool may have
		// executed in a parallel run whose result never landed.
		if !legacySequentialBuiltin(call.Name) || l.isConcurrentTool(call.Name) {
			status = "effect_pending"
		}
		batch.Calls[i] = ToolCallState{ResultEntryID: l.newID(), SourceIndex: i, Status: status}
	}
	if !missing {
		return nil, nil
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("harness: resume: checkpoint has missing tool results but no committed batch progress")
	}
	if err := l.commitOpState(operationID, OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: l.opSettings(), Batch: batch}); err != nil {
		return nil, err
	}
	return batch, nil
}

func legacySequentialBuiltin(name string) bool {
	switch name {
	case "bash", "read", "edit", "write":
		return true
	}
	return false
}
