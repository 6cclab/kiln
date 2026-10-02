package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tool"
)

// Frame is what one streamed provider event becomes when recorded to
// pi.pending.assistant_frame, so a crashed process could in principle
// replay it. This phase's Resume never actually replays frames (partial
// streaming state is always re-requested from scratch, see Resume), but
// the frames are still written so the on-disk write sequence matches pi's.
type Frame struct {
	Type         msg.EventType  `json:"type"`
	ContentIndex int            `json:"contentIndex"`
	Delta        string         `json:"delta,omitempty"`
	Content      string         `json:"content,omitempty"`
	ToolCall     *msg.ToolCall  `json:"toolCall,omitempty"`
	Reason       msg.StopReason `json:"reason,omitempty"`
}

// Prompt starts a new run operation: it appends a user message entry to
// the lane's branch and drives the turn loop (assistant request -> tool
// execution -> ... ) until the run stops.
func (l *Lane) Prompt(ctx context.Context, text string, images []msg.ImageContent) (RunResult, error) {
	return l.PromptAs(ctx, text, "", images)
}

// PromptAs is Prompt for a prompt a user typed: typed is the line as typed,
// recorded on the stored message (msg.UserMessage.KilnTyped) apart from
// whatever kiln built around it. "" records nothing.
func (l *Lane) PromptAs(ctx context.Context, text, typed string, images []msg.ImageContent) (RunResult, error) {
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
	// A Steer call between the previous operation ending and this one
	// starting (the between-turns case TestSteerDelivery exercises) left
	// its entry parked in the inbox, off the branch. Drain it here, before
	// this prompt's own entry is parented, so the queued text lands
	// ahead of the new prompt in the transcript rather than after it or
	// not at all — the same checkpoint the drive loop applies mid-run,
	// just reached from a fresh Prompt call instead of a loop iteration.
	var drainErr error
	if tip, _, drainErr = l.drainInbox(tip); drainErr != nil {
		return RunResult{}, drainErr
	}
	var parent *string
	if tip != "" {
		parent = &tip
	}

	blocks := msg.Blocks{}
	if text != "" {
		blocks = append(blocks, msg.Text(text))
	}
	for _, img := range images {
		blocks = append(blocks, img)
	}

	promptEntryID := l.newID()
	operationID := l.newID()
	now := l.now()

	userEntry := session.Entry{
		ID:       promptEntryID,
		ParentID: parent,
		Type:     session.EntryMessage,
		Message:  msg.UserMessage{Role: msg.RoleUser, Content: blocks, KilnTyped: typed, Timestamp: now},
	}
	entryWrite := session.EntryWrite{Entry: userEntry}
	tipWrite, err := session.SetValue(session.BranchTip(l.name), &promptEntryID)
	if err != nil {
		return RunResult{}, err
	}
	metaWrite, err := session.SetValue(opMetaAddr(operationID), OpMeta{
		Intent:      OpIntent{Kind: "run", PromptEntryIDs: []string{promptEntryID}},
		Lane:        l.name,
		OperationID: operationID,
		SourceTipID: parent,
		StartedAt:   now,
	})
	if err != nil {
		return RunResult{}, err
	}
	settings := l.opSettings()
	stateWrite, err := session.SetValue(opStateAddr(operationID), OpState{
		At:       AtStarting,
		Control:  OpControl{Status: "running"},
		Settings: settings,
	})
	if err != nil {
		return RunResult{}, err
	}
	laneState, err := l.laneState()
	if err != nil {
		return RunResult{}, err
	}
	laneState.CurrentOperationID = &operationID
	laneStateWrite, err := session.SetValue(session.LaneStateValue(l.name), laneState)
	if err != nil {
		return RunResult{}, err
	}

	if _, err := l.h.opts.Storage.Commit([]session.Write{entryWrite, tipWrite, metaWrite, stateWrite, laneStateWrite}); err != nil {
		return RunResult{}, err
	}
	l.h.events.Emit(Event{Type: EventEntryAdded, Lane: l.name, EntryID: promptEntryID, ParentID: derefOr(parent, "")})
	l.h.events.Emit(Event{Type: EventRunStart, Lane: l.name, OperationID: operationID})

	l.invokeBeforeRun(runCtx)

	result := l.drive(runCtx, operationID, promptEntryID)

	l.invokeBeforeRunEnd(runCtx, result.Status)
	l.h.events.Emit(Event{Type: EventRunEnd, Lane: l.name, OperationID: operationID, Status: result.Status, TipID: result.TipID})
	return result, result.Error
}

func derefOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

func (l *Lane) opSettings() OpSettings {
	c := l.h.opts.Compaction
	return OpSettings{
		Compaction: OpCompactionSettings{
			Enabled:          c.Enabled,
			KeepRecentTokens: c.KeepRecentTokens,
			ReserveTokens:    c.ReserveTokens,
		},
		FollowUpMode:  "all",
		SteeringMode:  "all",
		ToolExecution: "sequential",
	}
}

// drive is the operation's turn loop: checkpoint -> assistant request ->
// (tools -> checkpoint)* -> completion. tip is the branch tip as of the
// start of this iteration.
func (l *Lane) drive(ctx context.Context, operationID, tip string) RunResult {
	l.invokeBeforeDrive(ctx)
	firstIteration := true
	compactFirst := true
	// overflowRecoveryUsed: this operation has already compacted once
	// because a request did not fit; it does not loop on it (pi's
	// overflowRecoveryUsed).
	overflowRecoveryUsed := false
	for {
		if err := ctx.Err(); err != nil {
			return l.finishAborted(operationID, tip)
		}

		// A standalone "checkpoint" commit only happens on entry to the
		// loop (matching the reference session's line 4, right after
		// pi.op.state=starting). After a tool result, the checkpoint state
		// is already the last item of that toolResult's own commit (see
		// executeOneTool), so this loop must not re-emit it before
		// assistant.ready — doing so would insert a spurious extra
		// transaction the reference session never writes.
		if firstIteration {
			firstIteration = false
			if err := l.commitOpState(operationID, OpState{At: AtCheckpoint, Control: OpControl{Status: "running"},
				Settings: l.opSettings(), Continuation: &Continuation{Kind: "need_assistant"}}); err != nil {
				return RunResult{Status: StatusFailed, TipID: tip, Error: err}
			}
		}

		l.h.events.Emit(Event{Type: EventTurnStart, Lane: l.name, OperationID: operationID})

		// Checkpoint: drain any follow-ups Lane.Steer queued while the
		// previous iteration's request was in flight, before this
		// iteration reads the branch for its own transcript. This is the
		// "after a message end / before the next p.Stream" checkpoint —
		// a Steer call racing the in-flight request lands in the inbox,
		// not on the tip (see Steer's doc comment), so it must be
		// re-parented onto the tip here or the model never sees it.
		var drainErr error
		if tip, _, drainErr = l.drainInbox(tip); drainErr != nil {
			return l.finishFailed(operationID, tip, drainErr)
		}

		// Before an operation's first request, too: the end-of-turn check
		// below only runs between tool steps, so a conversation whose
		// turns end in text never compacted however full it got.
		if compactFirst {
			compactFirst = false
			tip = l.autoCompact(ctx, tip)
		}

		_, cfg, err := l.resolveModel()
		if err != nil {
			return l.finishFailed(operationID, tip, err)
		}

		// The request must fit the model's window: a provider like Ollama
		// does not refuse an oversized prompt, it truncates it silently.
		// fitRequest compacts once if it does not fit and refuses to send
		// one that still does not.
		var transcript []msg.Message
		tip, transcript, err = l.fitRequest(ctx, tip, &overflowRecoveryUsed)
		if err != nil {
			if ctx.Err() != nil {
				return l.finishAborted(operationID, tip)
			}
			return l.finishFailed(operationID, tip, err)
		}

		laneStateNow, _ := l.laneState()
		laneStateWrite, _ := session.SetValue(session.LaneStateValue(l.name), laneStateNow)
		if err := l.commitOpStateWith(operationID, OpState{At: AtAssistantReady, Control: OpControl{Status: "running"},
			Settings: l.opSettings(), Continuation: &Continuation{Kind: "need_assistant"}}, laneStateWrite); err != nil {
			return RunResult{Status: StatusFailed, TipID: tip, Error: err}
		}

		responseEntryID := l.newID()
		if err := l.commitOpState(operationID, OpState{At: AtAssistantEffectPending, Control: OpControl{Status: "running"},
			Settings: l.opSettings(), ResponseEntryID: &responseEntryID}); err != nil {
			return RunResult{Status: StatusFailed, TipID: tip, Error: err}
		}

		l.h.events.Emit(Event{Type: EventMessageStart, Lane: l.name, OperationID: operationID, EntryID: responseEntryID})

		final, err := l.requestWithRetry(ctx, operationID, transcript, cfg)
		if err != nil && ctx.Err() == nil && !overflowRecoveryUsed && isContextOverflow(err) {
			// The provider refused a request the estimate said would fit
			// (chars/4 undercounts dense text). Compact once and resend,
			// as pi and Claude Code do on an overflow error.
			overflowRecoveryUsed = true
			if newTip, compacted := l.compactForOverflow(ctx, tip); compacted {
				tip = newTip
				var fitErr error
				if tip, transcript, fitErr = l.fitRequest(ctx, tip, &overflowRecoveryUsed); fitErr != nil {
					err = fitErr
				} else {
					final, err = l.requestWithRetry(ctx, operationID, transcript, cfg)
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return l.finishAborted(operationID, tip)
			}
			return l.finishFailed(operationID, tip, err)
		}
		l.h.events.Emit(Event{Type: EventMessageEnd, Lane: l.name, OperationID: operationID, EntryID: responseEntryID, Message: final})
		l.invokeAfterResponse(ctx, final)

		toolCalls := msg.ToolCallsOf(final.Content)

		nextAt := AtCheckpoint
		if len(toolCalls) > 0 {
			nextAt = AtTools
		}
		assistantEntry := session.Entry{ID: responseEntryID, Type: session.EntryMessage, Message: *final}
		if tip != "" {
			p := tip
			assistantEntry.ParentID = &p
		}
		commitWrites := []session.Write{
			session.EntryWrite{Entry: assistantEntry},
			session.UsageWrite{Row: session.UsageRow{ID: l.newID(), Usage: final.Usage, EntryID: responseEntryID}},
		}
		tipW, err := session.SetValue(session.BranchTip(l.name), &responseEntryID)
		if err != nil {
			return l.finishFailed(operationID, tip, err)
		}
		commitWrites = append(commitWrites, tipW)
		commitWrites = append(commitWrites, session.DeleteListWrite(session.PendingAssistantFrames(operationID, responseEntryID)))
		stateVal := OpState{At: nextAt, Control: OpControl{Status: "running"}, Settings: l.opSettings()}
		if nextAt == AtTools {
			calls := make([]ToolCallState, len(toolCalls))
			for i := range toolCalls {
				calls[i] = ToolCallState{ResultEntryID: l.newID(), SourceIndex: i, Status: "planned"}
			}
			stateVal.Batch = &Batch{AssistantEntryID: responseEntryID, Calls: calls, TurnID: responseEntryID}
		}
		stateW, err := session.SetValue(opStateAddr(operationID), stateVal)
		if err != nil {
			return l.finishFailed(operationID, tip, err)
		}
		commitWrites = append(commitWrites, stateW)
		if _, err := l.h.opts.Storage.Commit(commitWrites); err != nil {
			return l.finishFailed(operationID, tip, err)
		}
		l.h.events.Emit(Event{Type: EventEntryAdded, Lane: l.name, EntryID: responseEntryID, ParentID: tip})
		totals := l.h.opts.Storage.GetStats().Usage
		l.h.events.Emit(Event{Type: EventUsage, Lane: l.name, OperationID: operationID, UsageRow: &final.Usage, UsageTotals: &totals})
		tip = responseEntryID

		if len(toolCalls) == 0 {
			if final.StopReason == msg.StopPause {
				// Anthropic's pause_turn: a long server-tool turn (e.g. an
				// extended web search) was cut for interim delivery, not
				// finished. The partial assistant message was already
				// committed to the branch tip above, so the next loop
				// iteration's transcript scan includes it verbatim; simply
				// looping back re-requests exactly as Anthropic's docs
				// prescribe, without treating this as the end of the turn.
				continue
			}
			l.h.events.Emit(Event{Type: EventTurnEnd, Lane: l.name, OperationID: operationID})
			// The model finished before the next loop iteration's own
			// checkpoint got a chance to drain the inbox. A queued
			// follow-up that only ever lands on the branch, with no
			// model turn of its own, is the same bug as one that never
			// reached the branch at all — so if anything was queued,
			// keep the operation running for one more model call
			// instead of finishing it.
			var drained bool
			var drainErr error
			if tip, drained, drainErr = l.drainInbox(tip); drainErr != nil {
				return l.finishFailed(operationID, tip, drainErr)
			}
			if drained {
				continue
			}
			return l.finishCompleted(operationID, tip)
		}

		// Partition toolCalls into maximal runs of consecutive calls whose
		// tool is Concurrent (see tool.Tool.Concurrent). A run of length 1
		// (concurrent or not) and every non-concurrent call still goes
		// through executeOneTool exactly as before P4; only a run of 2 or
		// more actually overlaps execution. batch is the whole turn's
		// []ToolCallState, threaded through every commit below so op.state
		// always carries every call's current status, not just the one
		// being touched.
		batch := stateVal.Batch.Calls
		for i := 0; i < len(toolCalls); {
			j := i + 1
			if l.isConcurrentTool(toolCalls[i].Name) {
				for j < len(toolCalls) && l.isConcurrentTool(toolCalls[j].Name) {
					j++
				}
			}
			var runErr error
			if j-i >= 2 {
				tip, runErr = l.executeConcurrentRun(ctx, operationID, tip, responseEntryID, toolCalls, batch, i, j)
			} else {
				tip, runErr = l.executeOneTool(ctx, operationID, tip, responseEntryID, toolCalls[i], i, batch)
			}
			if runErr != nil {
				if ctx.Err() != nil {
					return l.finishAborted(operationID, tip)
				}
				return l.finishFailed(operationID, tip, runErr)
			}
			i = j
		}
		l.h.events.Emit(Event{Type: EventTurnEnd, Lane: l.name, OperationID: operationID})
		tip = l.autoCompact(ctx, tip)
		// loop back for the next assistant turn.
	}
}

// drainInbox re-parents every entry queued via Lane.Steer since the last
// drain onto tip and clears pi.lane.state.inbox. A queued entry cannot be
// re-parented in place — commit.go's ValidateCommittedWrites rejects a
// duplicate entry id, and nothing in this codebase rewrites a committed
// entry's ParentID — so each item becomes a fresh entry that copies the
// queued message, parented onto the current tip, and the branch tip is
// advanced to it. It reports the resulting tip and whether anything was
// drained: a turn that ended with no tool calls must not finish the
// operation if drainInbox drained something, because a queued follow-up
// that lands on the branch but never gets its own model turn is exactly
// as broken as one that fell off the branch entirely.
func (l *Lane) drainInbox(tip string) (string, bool, error) {
	st, err := l.laneState()
	if err != nil {
		return tip, false, err
	}
	if len(st.Inbox) == 0 {
		return tip, false, nil
	}
	ids := make([]string, len(st.Inbox))
	for i, item := range st.Inbox {
		ids[i] = item.EntryID
	}
	queuedEntries := l.h.opts.Storage.GetEntries(ids)
	drained := false
	for _, item := range st.Inbox {
		queued, ok := queuedEntries[item.EntryID]
		if !ok {
			continue
		}
		newID := l.newID()
		var parent *string
		if tip != "" {
			p := tip
			parent = &p
		}
		entryWrite := session.EntryWrite{Entry: session.Entry{
			ID: newID, ParentID: parent, Type: queued.Type, CustomType: queued.CustomType, Message: queued.Message,
		}}
		tipWrite, err := session.SetValue(session.BranchTip(l.name), &newID)
		if err != nil {
			return tip, drained, err
		}
		if _, err := l.h.opts.Storage.Commit([]session.Write{entryWrite, tipWrite}); err != nil {
			return tip, drained, err
		}
		l.h.events.Emit(Event{Type: EventEntryAdded, Lane: l.name, EntryID: newID, ParentID: derefOr(parent, "")})
		tip = newID
		drained = true
	}
	st.Inbox = nil
	w, err := session.SetValue(session.LaneStateValue(l.name), st)
	if err != nil {
		return tip, drained, err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{w}); err != nil {
		return tip, drained, err
	}
	l.h.events.Emit(Event{Type: EventQueueUpdate, Lane: l.name, QueueLen: 0})
	return tip, drained, nil
}

func (l *Lane) commitOpState(operationID string, st OpState) error {
	w, err := session.SetValue(opStateAddr(operationID), st)
	if err != nil {
		return err
	}
	_, err = l.h.opts.Storage.Commit([]session.Write{w})
	return err
}

func (l *Lane) commitOpStateWith(operationID string, st OpState, extra ...session.Write) error {
	w, err := session.SetValue(opStateAddr(operationID), st)
	if err != nil {
		return err
	}
	writes := append([]session.Write{w}, extra...)
	_, err = l.h.opts.Storage.Commit(writes)
	return err
}

func (l *Lane) finishCompleted(operationID, tip string) RunResult {
	l.finishOperation(operationID, tip, StatusCompleted, "")
	return RunResult{Status: StatusCompleted, TipID: tip}
}

func (l *Lane) finishAborted(operationID, tip string) RunResult {
	l.finishOperation(operationID, tip, StatusAborted, "")
	return RunResult{Status: StatusAborted, TipID: tip}
}

func (l *Lane) finishFailed(operationID, tip string, err error) RunResult {
	l.finishOperation(operationID, tip, StatusFailed, err.Error())
	l.h.events.Emit(Event{Type: EventFault, Lane: l.name, OperationID: operationID, Err: err})
	return RunResult{Status: StatusFailed, TipID: tip, Error: err}
}

// finishOperation writes the terminal transaction: delete pi.op.meta,
// delete pi.op.state, set pi.result, and idle pi.lane.state — matching the
// four-item final transaction in the reference session.
func (l *Lane) finishOperation(operationID, tip, status, errMsg string) {
	var tipPtr *string
	if tip != "" {
		tipPtr = &tip
	}
	metaDel := session.DeleteValue(opMetaAddr(operationID))
	stateDel := session.DeleteValue(opStateAddr(operationID))
	resultW, _ := session.SetValue(opResultAddr(operationID), OpResult{
		EndedAt:     l.now(),
		Kind:        "run",
		OperationID: operationID,
		StartedAt:   l.now(),
		Status:      status,
		TipID:       tipPtr,
		Error:       errMsg,
	})
	laneState, _ := l.laneState()
	laneState.CurrentOperationID = nil
	laneState.LastOperationID = &operationID
	laneStateW, _ := session.SetValue(session.LaneStateValue(l.name), laneState)
	_, _ = l.h.opts.Storage.Commit([]session.Write{metaDel, stateDel, resultW, laneStateW})
}

// requestWithRetry streams one assistant response, retrying on retriable
// provider errors per l.h.opts.Retry, and records every stream event as a
// pi.pending.assistant_frame append.
func (l *Lane) requestWithRetry(ctx context.Context, operationID string, transcript []msg.Message, cfg session.LaneConfiguration) (*msg.AssistantMessage, error) {
	l.invokeBeforeRequest(ctx)
	p, ok := l.h.opts.Registry.Provider(cfg.Model.Provider)
	if !ok {
		return nil, fmt.Errorf("harness: unknown provider %q", cfg.Model.Provider)
	}
	m, ok := l.h.opts.Registry.GetModel(cfg.Model.Provider, cfg.Model.ModelID)
	if !ok {
		return nil, fmt.Errorf("harness: unknown model %s/%s", cfg.Model.Provider, cfg.Model.ModelID)
	}
	opts := buildStreamOptions(l.h.opts, cfg)

	responseEntryID := ""
	if raw, _, ok := l.h.opts.Storage.GetValue(session.NamespaceOpState, operationID); ok {
		var st OpState
		if json.Unmarshal(raw, &st) == nil && st.ResponseEntryID != nil {
			responseEntryID = *st.ResponseEntryID
		}
	}

	retry := l.h.opts.Retry
	var lastErr error
	for attempt := 1; attempt <= retry.MaxAttempts; attempt++ {
		if attempt > 1 && responseEntryID != "" {
			// The cut attempt's frames are not this response: a crash-resume
			// replaying the pending list would otherwise stitch two partial
			// answers together.
			_, _ = l.h.opts.Storage.Commit([]session.Write{session.DeleteListWrite(session.PendingAssistantFrames(operationID, responseEntryID))})
		}
		events, wait := p.Stream(ctx, m, transcript, opts)
		for ev := range events {
			if responseEntryID != "" && ev.Type != msg.EventStart {
				frame := Frame{Type: ev.Type, ContentIndex: ev.ContentIndex, Delta: ev.Delta, Content: ev.Content, ToolCall: ev.ToolCall, Reason: ev.Reason}
				w, _ := session.AppendListWrite(session.PendingAssistantFrames(operationID, responseEntryID), json.RawMessage(mustMarshal(frame)))
				_, _ = l.h.opts.Storage.Commit([]session.Write{w})
			}
			l.h.events.Emit(Event{Type: EventMessageUpdate, Lane: l.name, OperationID: operationID, EntryID: responseEntryID, StreamEvent: &ev})
		}
		final, err := wait()
		if err == nil {
			return final, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, err
		}
		if !isRetriable(err) || attempt == retry.MaxAttempts {
			return nil, err
		}
		delay := retry.delay(attempt)
		l.h.events.Emit(Event{Type: EventRetryScheduled, Lane: l.name, OperationID: operationID, Attempt: attempt, MaxAttempts: retry.MaxAttempts, DelayMs: delay.Milliseconds(), RetryError: err.Error()})
		if sleepErr := sleepCtx(ctx, delay, l.retryNow); sleepErr != nil {
			return nil, sleepErr
		}
		l.h.events.Emit(Event{Type: EventRetryStart, Lane: l.name, OperationID: operationID, Attempt: attempt + 1})
	}
	return nil, lastErr
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

func buildStreamOptions(opts Options, cfg session.LaneConfiguration) provider.StreamOptions {
	thinking := cfg.ThinkingLevel
	if thinking == "" {
		thinking = opts.ThinkingLevel
	}
	var toolDefs []provider.ToolDef
	for _, name := range cfg.ActiveToolNames {
		t, ok := opts.Tools.Get(name)
		if !ok {
			continue
		}
		toolDefs = append(toolDefs, provider.ToolDef{Name: t.Name, Description: t.Description, Parameters: t.Parameters, ServerTool: t.ServerTool})
	}
	return provider.StreamOptions{
		ThinkingLevel: provider.ThinkingLevel(thinking),
		Tools:         toolDefs,
		SystemPrompt:  opts.SystemPrompt,
	}
}

// isConcurrentTool reports whether name resolves to a tool marked
// Concurrent. An unknown tool name is treated as non-concurrent: it will
// hit the "unknown tool" error path in beginTool exactly as it always did,
// alone in its own run.
func (l *Lane) isConcurrentTool(name string) bool {
	t, ok := l.h.opts.Tools.Get(name)
	return ok && t.Concurrent
}

// executeOneTool runs one tool call start to finish on the calling
// goroutine: commitToolPending, beginTool, commitToolResult. It is the
// sequential path used by drive() for a run of length 1 and by
// resume.go's re-run of a not-yet-finished tool call. batch is the whole
// turn's []ToolCallState; sourceIndex is this call's position in it.
func (l *Lane) executeOneTool(ctx context.Context, operationID, tip, assistantEntryID string, call msg.ToolCall, sourceIndex int, batch []ToolCallState) (string, error) {
	if err := l.commitToolPending(operationID, assistantEntryID, call, sourceIndex, batch); err != nil {
		return tip, err
	}
	result, permOutcome, err := l.beginTool(ctx, operationID, call)
	if err != nil {
		return tip, err
	}
	return l.commitToolResult(ctx, operationID, tip, assistantEntryID, call, sourceIndex, result, permOutcome, batch)
}

// executeConcurrentRun runs toolCalls[start:end] — a maximal run of
// consecutive calls to Concurrent-safe tools from one assistant message —
// in parallel: commitToolPending for each call in source order on the
// driving goroutine, then one goroutine per call running beginTool (hooks
// + Execute, no Storage.Commit), then commitToolResult for each call in
// source order on the driving goroutine again, chaining tip. Committing
// only ever happens on the driving goroutine because
// jsonl.Storage.Commit is not safe to call from more than one goroutine at
// a time; only beginTool (which never commits) runs concurrently.
//
// If ctx is canceled, or a beginTool goroutine otherwise errors, that is
// handled once every goroutine has finished (never mid-flight): the
// results of the calls before the first failure are committed, in source
// order, and the first error is returned.
func (l *Lane) executeConcurrentRun(ctx context.Context, operationID, tip, assistantEntryID string, toolCalls []msg.ToolCall, batch []ToolCallState, start, end int) (string, error) {
	for idx := start; idx < end; idx++ {
		if err := l.commitToolPending(operationID, assistantEntryID, toolCalls[idx], idx, batch); err != nil {
			return tip, err
		}
	}

	n := end - start
	results := make([]msg.ToolResultMessage, n)
	permOutcomes := make([]string, n)
	beginErrs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for k := 0; k < n; k++ {
		go func(k int) {
			defer wg.Done()
			results[k], permOutcomes[k], beginErrs[k] = l.beginTool(ctx, operationID, toolCalls[start+k])
		}(k)
	}
	wg.Wait()

	for k := 0; k < n; k++ {
		if err := beginErrs[k]; err != nil {
			return tip, err
		}
		idx := start + k
		var err error
		tip, err = l.commitToolResult(ctx, operationID, tip, assistantEntryID, toolCalls[idx], idx, results[k], permOutcomes[k], batch)
		if err != nil {
			return tip, err
		}
	}
	return tip, nil
}

// commitToolPending writes the first of a tool call's three transactions:
// tool_args set and op.state=tools with this call's status advanced to
// "effect_pending" in the whole-batch snapshot. No hook or Execute has run
// yet.
func (l *Lane) commitToolPending(operationID, assistantEntryID string, call msg.ToolCall, sourceIndex int, batch []ToolCallState) error {
	argsRaw, _ := json.Marshal(call.Arguments)
	argsW := session.SetValueRaw(session.NamespaceOpToolArgs, operationID+":"+assistantEntryID+":"+itoa(sourceIndex), argsRaw)
	batch[sourceIndex].Status = "effect_pending"
	stateW, err := session.SetValue(opStateAddr(operationID), OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: l.opSettings(),
		Batch: &Batch{AssistantEntryID: assistantEntryID, TurnID: assistantEntryID, Calls: batch}})
	if err != nil {
		return err
	}
	_, err = l.h.opts.Storage.Commit([]session.Write{argsW, stateW})
	return err
}

// beginTool runs the before_tool hook, the args rewrite it may request,
// and the tool's own Execute, and builds the resulting ToolResultMessage.
// It performs no Storage.Commit, which is what makes it safe to run on a
// goroutine of its own for a Concurrent run: every write for this call
// happens later, back on the driving goroutine, in commitToolResult.
func (l *Lane) beginTool(ctx context.Context, operationID string, call msg.ToolCall) (msg.ToolResultMessage, string, error) {
	if err := ctx.Err(); err != nil {
		return msg.ToolResultMessage{}, "", err
	}
	l.h.events.Emit(Event{Type: EventToolStart, Lane: l.name, OperationID: operationID, ToolCallID: call.ID, ToolName: call.Name, ToolArgs: call.Arguments})

	// A call that cannot run is refused before the before-tool hooks: they
	// include the permission gate, and asking the user to approve a call
	// that is then refused anyway wastes their answer.
	var refusal *tool.Result
	if _, registered := l.h.opts.Tools.Get(call.Name); !registered {
		// Distinct from the "not in the active tool set" branch below: a
		// name that was never registered anywhere is a typo or a
		// hallucinated tool, not an access restriction, and the two read
		// very differently to a user watching the transcript.
		diag.L().Info("tool refused: unknown", "lane", l.name, "tool", call.Name)
		r := tool.Errorf("There is no tool named %s, so the call did not run.", call.Name)
		refusal = &r
	} else if !l.toolActive(call.Name) {
		active, _ := l.GetActiveTools()
		diag.L().Info("tool refused: not active", "lane", l.name, "tool", call.Name, "active", active)
		// The active set is not only what the model is offered: a subagent
		// restricted to Read must not run bash because its model guessed
		// the name, and a gated MCP tool must be activated through
		// tool_search before it executes. Refusing here, not just in the
		// schema, is what makes an allowlist an allowlist.
		r := tool.Errorf("tool %q is not available to this agent: it is not in the active tool set", call.Name)
		refusal = &r
	}

	// Input the permission gate and the tool would read differently never
	// reaches either: the gate and the PreToolUse hooks read keys from the
	// map exactly, while the tools decode it with encoding/json, which
	// matches keys case-insensitively (see internal/tool/argcheck.go).
	// Checked here, before every before_tool hook, because this is the one
	// place every tool call passes through: top-level lanes, subagents
	// (each runs its own Harness through this same loop) and resumed calls
	// alike.
	if refusal == nil && call.InvalidArgs == "" {
		t, _ := l.h.opts.Tools.Get(call.Name)
		if err := tool.CheckArgsMapFor(t, call.Arguments); err != nil {
			diag.L().Info("tool refused: ambiguous input", "lane", l.name, "tool", call.Name, "reason", err.Error())
			r := argsRefusal(call.Name, err)
			refusal = &r
		}
	}

	var before BeforeToolResult
	if refusal == nil {
		before = l.invokeBeforeTool(ctx, call)
	}

	args := call.Arguments
	if refusal == nil && before.Block == nil && before.RewrittenArgs != nil {
		// A PreToolUse hook rewrote the input. The gate judged the
		// rewritten input (claudehooks.GuardToolCall checks it with
		// Harness.CheckToolArgs before the gate runs); this re-check makes
		// it impossible for the tool to run anything else. Falling back to
		// the original input on an unparsable rewrite, as this once did,
		// would run what the gate never judged.
		t, _ := l.h.opts.Tools.Get(call.Name)
		var rewritten map[string]any
		if err := tool.CheckArgsFor(t, before.RewrittenArgs); err != nil {
			r := argsRefusal(call.Name, err)
			refusal = &r
		} else if err := json.Unmarshal(before.RewrittenArgs, &rewritten); err != nil {
			r := tool.Errorf("The call to %s did not run: a hook rewrote its input into something that is not a JSON object.", call.Name)
			refusal = &r
		} else {
			args = rewritten
		}
	}

	var result tool.Result
	if refusal != nil {
		result = *refusal
	} else if before.Block != nil {
		result = tool.Result{Content: msg.Blocks{msg.Text(before.Block.Reason)}, IsError: true}
	} else if call.InvalidArgs != "" {
		// The provider could not parse the call's arguments; running the
		// tool with empty arguments would silently do the wrong thing.
		result = tool.Errorf("The call's arguments were not valid JSON, so it did not run. Received: %s", call.InvalidArgs)
	} else {
		// Registration was already confirmed above, so Get cannot miss
		// here; it is repeated rather than threading the *tool.Tool
		// through the branches above.
		t, _ := l.h.opts.Tools.Get(call.Name)
		argsJSON, _ := json.Marshal(args)
		res, execErr := t.Execute(ctx, argsJSON, func(tool.Result) {}, tool.Invocation{ToolCallID: call.ID, ToolName: call.Name, Cwd: l.h.opts.Cwd})
		if execErr != nil {
			result = tool.Errorf("%s", execErr.Error())
		} else {
			result = res
		}
	}

	return msg.ToolResultMessage{
		Content:    truncateToolResult(sanitizeToolResult(result.Content), l.h.opts.ToolOutputTokens),
		Details:    result.Details,
		IsError:    result.IsError,
		Role:       msg.RoleToolResult,
		Timestamp:  l.now(),
		ToolCallID: call.ID,
		ToolName:   call.Name,
	}, before.PermissionOutcome, nil
}

// argsRefusal is the tool result for input tool.CheckArgs refused.
func argsRefusal(toolName string, err error) tool.Result {
	return tool.Errorf("The call to %s did not run: %s. Send each parameter once, spelled exactly as the tool declares it.", toolName, err.Error())
}

// CheckToolArgs runs tool.CheckArgs on args against the named tool's
// schema, the same check beginTool applies to every call. A PreToolUse
// hook wrapper calls it on hook-rewritten input before the permission
// gate judges that input. An unknown tool name passes: beginTool refuses
// those itself, before any hook runs.
func (h *Harness) CheckToolArgs(name string, args map[string]any) error {
	t, ok := h.opts.Tools.Get(name)
	if !ok {
		return nil
	}
	return tool.CheckArgsMapFor(t, args)
}

// commitToolResult writes a tool call's remaining two transactions: the
// pending.entry set / pending.tool_output delete / op.state="outcome_ready"
// commit, then the toolResult entry + branch tip + op.state=checkpoint
// commit. Both commits carry the whole-batch []ToolCallState snapshot with
// this call's entry updated. It emits entry_added, runs after_tool, emits
// tool_end, and returns the branch's new tip (the toolResult entry's id).
func (l *Lane) commitToolResult(ctx context.Context, operationID, tip, assistantEntryID string, call msg.ToolCall, sourceIndex int, toolResultMsg msg.ToolResultMessage, permOutcome string, batch []ToolCallState) (string, error) {
	resultEntryID := batch[sourceIndex].ResultEntryID

	pendingRaw, err := json.Marshal(PendingToolResultPayload{Payload: toolResultMsg})
	if err != nil {
		return tip, err
	}
	pendingW, err := session.SetValue(session.PendingEntry(resultEntryID), json.RawMessage(pendingRaw))
	if err != nil {
		return tip, err
	}
	toolOutputDel := session.DeleteValue(session.PendingToolOutput(operationID, resultEntryID))
	terminate := false
	batch[sourceIndex].Status = "outcome_ready"
	batch[sourceIndex].Terminate = &terminate
	stateW2, err := session.SetValue(opStateAddr(operationID), OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: l.opSettings(),
		Batch: &Batch{AssistantEntryID: assistantEntryID, TurnID: assistantEntryID, Calls: batch}})
	if err != nil {
		return tip, err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{pendingW, toolOutputDel, stateW2}); err != nil {
		return tip, err
	}

	parentTip := tip
	resultEntry := session.Entry{ID: resultEntryID, ParentID: &parentTip, Type: session.EntryMessage, Message: toolResultMsg}
	pendingDel := session.DeleteValue(session.PendingEntry(resultEntryID))
	tipW, err := session.SetValue(session.BranchTip(l.name), &resultEntryID)
	if err != nil {
		return tip, err
	}
	argsDel := session.DeleteValue(session.OperationToolArgs(operationID, assistantEntryID, sourceIndex))
	stateW3, err := session.SetValue(opStateAddr(operationID), OpState{At: AtCheckpoint, Control: OpControl{Status: "running"}, Settings: l.opSettings(),
		Continuation: &Continuation{Kind: "need_assistant"}, LatestAssistantEntryID: &assistantEntryID})
	if err != nil {
		return tip, err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{
		session.EntryWrite{Entry: resultEntry}, pendingDel, tipW, argsDel, stateW3,
	}); err != nil {
		return tip, err
	}
	l.h.events.Emit(Event{Type: EventEntryAdded, Lane: l.name, EntryID: resultEntryID, ParentID: parentTip})
	l.invokeAfterTool(ctx, call, &toolResultMsg)
	l.h.events.Emit(Event{Type: EventToolEnd, Lane: l.name, OperationID: operationID, ToolCallID: call.ID, ToolName: call.Name, ToolArgs: call.Arguments, ToolResult: &toolResultMsg, PermissionOutcome: permOutcome})

	// The toolResult commit above already carries op.state=checkpoint as
	// its last item (matching the reference session's line 35); the
	// drive() loop's next iteration writes assistant.ready and
	// assistant.effect_pending itself (lines 36-37) without a redundant
	// standalone checkpoint in between.
	return resultEntryID, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// toolActive reports whether name is in the lane's active tool set. A lane
// with no recorded configuration (older sessions) or an empty set is not
// filtered, matching buildStreamOptions' "offer nothing" only for the
// schema side; execution stays permissive there so a resumed legacy
// session keeps working.
func (l *Lane) toolActive(name string) bool {
	cfg, err := l.config()
	if err != nil || len(cfg.ActiveToolNames) == 0 {
		return true
	}
	for _, n := range cfg.ActiveToolNames {
		if n == name {
			return true
		}
	}
	return false
}
