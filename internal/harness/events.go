package harness

import (
	"sync"
	"sync/atomic"

	"github.com/andrepato/harness/internal/msg"
)

// EventType is the discriminator on Event, covering all 34 pi harness
// event variants (agent-harness.d.ts HarnessEvent).
type EventType string

const (
	EventRunStart   EventType = "run_start"
	EventRunResume  EventType = "run_resume"
	EventRunSuspend EventType = "run_suspend"
	EventRunEnd     EventType = "run_end"
	EventTurnStart  EventType = "turn_start"
	EventTurnEnd    EventType = "turn_end"

	EventMessageStart  EventType = "message_start"
	EventMessageUpdate EventType = "message_update"
	EventMessageEnd    EventType = "message_end"

	EventToolStart  EventType = "tool_start"
	EventToolUpdate EventType = "tool_update"
	EventToolEnd    EventType = "tool_end"

	EventRetryScheduled EventType = "retry_scheduled"
	EventRetryStart     EventType = "retry_start"
	EventRetryEnd       EventType = "retry_end"

	EventCompactionStart EventType = "compaction_start"
	EventCompactionEnd   EventType = "compaction_end"
	// EventCompactionProgress reports a running compaction's part and
	// streamed output (kiln's own; pi has no equivalent).
	EventCompactionProgress EventType = "compaction_progress"
	// EventCompactionRetry reports that a compaction request failed for a
	// transient reason and is being sent again (kiln's own): Attempt is
	// the attempt starting, RetryError the reason in plain words, Err the
	// error itself.
	EventCompactionRetry EventType = "compaction_retry"

	EventNavigationStart EventType = "navigation_start"
	EventNavigationEnd   EventType = "navigation_end"

	EventEntryAdded     EventType = "entry_added"
	EventLaneCreated    EventType = "lane_created"
	EventQueueUpdate    EventType = "queue_update"
	EventOperationAbort EventType = "operation_abort"

	EventConfigUpdate EventType = "config_update"
	EventValueUpdate  EventType = "value_update"

	EventFault        EventType = "fault"
	EventHandlerError EventType = "handler_error"
	EventUsage        EventType = "usage"
)

// ConfigProperty names the field config_update reports as having changed.
type ConfigProperty string

const (
	ConfigModel              ConfigProperty = "model"
	ConfigThinkingLevel      ConfigProperty = "thinkingLevel"
	ConfigActiveTools        ConfigProperty = "activeTools"
	ConfigTools              ConfigProperty = "tools"
	ConfigResources          ConfigProperty = "resources"
	ConfigStreamOptions      ConfigProperty = "streamOptions"
	ConfigRetryPolicy        ConfigProperty = "retryPolicy"
	ConfigCompactionSettings ConfigProperty = "compactionSettings"
	ConfigSystemPrompt       ConfigProperty = "systemPrompt"
	ConfigSteeringMode       ConfigProperty = "steeringMode"
	ConfigFollowUpMode       ConfigProperty = "followUpMode"
)

// ValueNamespace names the field value_update reports as having changed.
type ValueNamespace string

const (
	ValueSessionName ValueNamespace = "session_name"
	ValueEntryLabel  ValueNamespace = "entry_label"
)

// Event is one flat superset of every pi harness event payload, tagged by
// Type. Only the fields relevant to Type are populated. This trades a
// larger struct for a hot path with no per-variant allocation or type
// assertion, as the plan specifies.
type Event struct {
	Type EventType
	Lane string

	OperationID string
	EntryID     string
	ParentID    string

	// message_update
	StreamEvent *msg.StreamEvent
	// message_end: the completed assistant message, as pi's message_end carries.
	Message *msg.AssistantMessage

	// tool_start/tool_update/tool_end
	ToolCallID string
	ToolName   string
	ToolArgs   map[string]any
	ToolResult *msg.ToolResultMessage
	// PermissionOutcome is set on tool_end only (not tool_start: the gate
	// has not run yet when tool_start fires — see beginTool in turn.go),
	// mirroring permission.Outcome as a plain string: "approved",
	// "auto-approved", or "" when no gate decision applies (a read-only
	// tool) or the call was blocked/denied.
	PermissionOutcome string

	// retry_*
	Attempt     int
	MaxAttempts int
	DelayMs     int64
	RetryError  string

	// compaction_*
	CompactionSummary string
	// CompactionTrigger is "manual" (/compact), "auto" (the context neared
	// the window) or "overflow" (the next request would not fit), on
	// compaction_start.
	CompactionTrigger string
	// CompactionModel is "provider/id" of the summarising model.
	CompactionModel string
	// CompactionPart/CompactionParts: the request being sent, 1-based, and
	// how many the compaction plans (compaction_progress).
	CompactionPart, CompactionParts int
	// CompactionPromptTokens/CompactionOutputTokens: the part's estimated
	// prompt size and what it has streamed back so far (compaction_progress).
	CompactionPromptTokens, CompactionOutputTokens int

	// navigation_*
	TargetEntryID string

	// config_update
	ConfigProperty ConfigProperty

	// value_update
	ValueNamespace ValueNamespace
	ValueKey       string

	// fault / handler_error
	Err      error
	HookName string

	// usage
	UsageRow    *msg.Usage
	UsageTotals *msg.Usage

	// run_end / turn_end
	Status string // "completed" | "aborted" | "failed"
	TipID  string

	// queue_update
	QueueLen int
}

// Events is a synchronous, in-process pub/sub bus for Event. Handlers run
// on the emitting goroutine (the lane's turn loop), in subscription order;
// a slow or blocking handler will therefore delay the loop, matching pi's
// own synchronous EventEmitter semantics.
//
// # Concurrency
//
// Since P4, Emit can be called from more than one goroutine at once: a
// Concurrent-tool run in the turn loop emits tool_start/tool_end from a
// goroutine per tool call. Emit stays synchronous for every existing
// caller (nothing needed a Flush/Wait added) by serializing handler
// invocation itself: invokeMu is held for the whole "run every matching
// handler" section of one Emit call, so handlers still run one at a time,
// in subscription order, and two concurrent Emit calls never interleave
// their handlers. subMu is a separate, narrower lock that only protects
// the subscription lists (On/OnAll/unsubscribe and the snapshot Emit takes
// of them) and is never held while a handler runs, so a handler that calls
// On/OnAll/unsubscribe cannot deadlock against Emit. A handler that itself
// calls Emit on the same goroutine (re-entrant emit) would deadlock on
// invokeMu; grep of internal/harness, internal/tui and internal/cli at the
// time this was written found no such handler, so the simpler
// non-reentrant design was chosen over a per-goroutine reentrancy token.
type Events struct {
	subMu     sync.Mutex
	byType    map[EventType][]subscription
	all       []subscription
	nextToken int64

	invokeMu sync.Mutex
}

type subscription struct {
	token int64
	fn    func(Event)
}

// NewEvents returns an empty Events bus.
func NewEvents() *Events {
	return &Events{byType: map[EventType][]subscription{}}
}

// On registers fn to run for every event of the given type. The returned
// func unsubscribes it.
func (e *Events) On(t EventType, fn func(Event)) func() {
	tok := atomic.AddInt64(&e.nextToken, 1)
	e.subMu.Lock()
	e.byType[t] = append(e.byType[t], subscription{token: tok, fn: fn})
	e.subMu.Unlock()
	return func() { e.unsubscribeType(t, tok) }
}

// OnAll registers fn to run for every event, of any type, in emission
// order. The returned func unsubscribes it.
func (e *Events) OnAll(fn func(Event)) func() {
	tok := atomic.AddInt64(&e.nextToken, 1)
	e.subMu.Lock()
	e.all = append(e.all, subscription{token: tok, fn: fn})
	e.subMu.Unlock()
	return func() { e.unsubscribeAll(tok) }
}

func (e *Events) unsubscribeType(t EventType, tok int64) {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	subs := e.byType[t]
	for i, s := range subs {
		if s.token == tok {
			e.byType[t] = append(subs[:i:i], subs[i+1:]...)
			return
		}
	}
}

func (e *Events) unsubscribeAll(tok int64) {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	for i, s := range e.all {
		if s.token == tok {
			e.all = append(e.all[:i:i], e.all[i+1:]...)
			return
		}
	}
}

// Emit runs every matching handler synchronously, in subscription order:
// type-specific handlers first, then OnAll handlers, matching pi's
// emitter.emit(type, ...) then emitter.emit('*', ...) order. invokeMu
// serializes the actual handler calls across concurrent Emit callers (see
// the Events doc comment); subMu, held only long enough to snapshot the
// subscriber lists, is released before any handler runs.
func (e *Events) Emit(ev Event) {
	e.subMu.Lock()
	byType := append([]subscription(nil), e.byType[ev.Type]...)
	all := append([]subscription(nil), e.all...)
	e.subMu.Unlock()

	e.invokeMu.Lock()
	defer e.invokeMu.Unlock()
	for _, s := range byType {
		s.fn(ev)
	}
	for _, s := range all {
		s.fn(ev)
	}
}
