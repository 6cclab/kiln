package harness

import (
	"context"
	"encoding/json"
	"fmt"

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
		Message:  msg.UserMessage{Role: msg.RoleUser, Content: blocks, Timestamp: now},
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

		_, cfg, err := l.resolveModel()
		if err != nil {
			return l.finishFailed(operationID, tip, err)
		}

		entries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "oldestFirst"})
		if err != nil {
			return l.finishFailed(operationID, tip, err)
		}
		transcript := entriesToTranscript(entries)
		transcript = l.invokeTransformContext(ctx, transcript)

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
			l.h.events.Emit(Event{Type: EventTurnEnd, Lane: l.name, OperationID: operationID})
			return l.finishCompleted(operationID, tip)
		}

		for i, call := range toolCalls {
			resultEntryID := stateVal.Batch.Calls[i].ResultEntryID
			var runErr error
			tip, runErr = l.executeOneTool(ctx, operationID, tip, responseEntryID, call, resultEntryID, i)
			if runErr != nil {
				if ctx.Err() != nil {
					return l.finishAborted(operationID, tip)
				}
				return l.finishFailed(operationID, tip, runErr)
			}
		}
		l.h.events.Emit(Event{Type: EventTurnEnd, Lane: l.name, OperationID: operationID})
		tip = l.autoCompact(ctx, tip)
		// loop back for the next assistant turn.
	}
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
		if sleepErr := sleepCtx(ctx, delay); sleepErr != nil {
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
		toolDefs = append(toolDefs, provider.ToolDef{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return provider.StreamOptions{
		ThinkingLevel: provider.ThinkingLevel(thinking),
		Tools:         toolDefs,
		SystemPrompt:  opts.SystemPrompt,
	}
}

// executeOneTool runs the before_tool hook, the tool itself, the
// after_tool hook, and commits the write sequence pi records for one tool
// call: tool_args set -> (execute) -> pending.entry set/pending.tool_output
// delete -> toolResult entry committed. It returns the branch's new tip.
func (l *Lane) executeOneTool(ctx context.Context, operationID, tip, assistantEntryID string, call msg.ToolCall, resultEntryID string, sourceIndex int) (string, error) {
	l.h.events.Emit(Event{Type: EventToolStart, Lane: l.name, OperationID: operationID, ToolCallID: call.ID, ToolName: call.Name, ToolArgs: call.Arguments})

	argsRaw, _ := json.Marshal(call.Arguments)
	argsW := session.SetValueRaw(session.NamespaceOpToolArgs, operationID+":"+assistantEntryID+":"+itoa(sourceIndex), argsRaw)
	stateW, err := session.SetValue(opStateAddr(operationID), OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: l.opSettings(),
		Batch: &Batch{AssistantEntryID: assistantEntryID, TurnID: assistantEntryID, Calls: []ToolCallState{{ResultEntryID: resultEntryID, SourceIndex: sourceIndex, Status: "effect_pending"}}}})
	if err != nil {
		return tip, err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{argsW, stateW}); err != nil {
		return tip, err
	}

	before := l.invokeBeforeTool(ctx, call)

	args := call.Arguments
	if before.RewrittenArgs != nil {
		var rewritten map[string]any
		if err := json.Unmarshal(before.RewrittenArgs, &rewritten); err == nil {
			args = rewritten
		}
	}

	var result tool.Result
	if before.Block != nil {
		result = tool.Result{Content: msg.Blocks{msg.Text(before.Block.Reason)}, IsError: true}
	} else if t, ok := l.h.opts.Tools.Get(call.Name); ok {
		argsJSON, _ := json.Marshal(args)
		res, execErr := t.Execute(ctx, argsJSON, func(tool.Result) {}, tool.Invocation{ToolCallID: call.ID, ToolName: call.Name, Cwd: l.h.opts.Cwd})
		if execErr != nil {
			result = tool.Errorf("%s", execErr.Error())
		} else {
			result = res
		}
	} else {
		result = tool.Errorf("unknown tool %q", call.Name)
	}

	toolResultMsg := msg.ToolResultMessage{
		Content:    result.Content,
		Details:    result.Details,
		IsError:    result.IsError,
		Role:       msg.RoleToolResult,
		Timestamp:  l.now(),
		ToolCallID: call.ID,
		ToolName:   call.Name,
	}

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
	stateW2, err := session.SetValue(opStateAddr(operationID), OpState{At: AtTools, Control: OpControl{Status: "running"}, Settings: l.opSettings(),
		Batch: &Batch{AssistantEntryID: assistantEntryID, TurnID: assistantEntryID, Calls: []ToolCallState{{ResultEntryID: resultEntryID, SourceIndex: sourceIndex, Status: "outcome_ready", Terminate: &terminate}}}})
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
	l.h.events.Emit(Event{Type: EventToolEnd, Lane: l.name, OperationID: operationID, ToolCallID: call.ID, ToolName: call.Name, ToolResult: &toolResultMsg})

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
