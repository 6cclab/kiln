package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
)

// RunResult is what Prompt/Steer/Compact/NavigateTree/Resume return: the
// terminal status of one operation, mirroring pi's OperationResult.
type RunResult struct {
	Status string // "completed" | "aborted" | "failed"
	TipID  string
	Error  error
}

const (
	StatusCompleted = "completed"
	StatusAborted   = "aborted"
	StatusFailed    = "failed"
)

// Lane drives one branch of one session through the operation FSM.
type Lane struct {
	h    *Harness
	name string

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool

	// retryNow is signalled by RetryNow to cut a pending retry backoff
	// short (the TUI's `r` key, docs/kiln-design-handoff/README.md's error
	// block: "r to retry now"). Buffered 1 so a signal sent while no retry
	// is pending is not lost as a blocking send, and so a second signal
	// before the first is drained does not block RetryNow's caller either
	// — sleepCtx only ever needs to observe one pending signal.
	retryNow chan struct{}
}

func (l *Lane) newID() string { return uuid.NewString() }

func (l *Lane) now() int64 { return l.h.opts.Now().UnixMilli() }

// config reads the lane's current configuration from storage.
func (l *Lane) config() (session.LaneConfiguration, error) {
	raw, _, ok := l.h.opts.Storage.GetValue(session.NamespaceLaneConfig, l.name)
	if !ok {
		return session.LaneConfiguration{}, fmt.Errorf("harness: lane %q has no configuration", l.name)
	}
	var cfg session.LaneConfiguration
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return session.LaneConfiguration{}, err
	}
	return cfg, nil
}

func (l *Lane) laneState() (session.LaneState, error) {
	raw, _, ok := l.h.opts.Storage.GetValue(session.NamespaceLaneState, l.name)
	if !ok {
		return session.LaneState{}, fmt.Errorf("harness: lane %q has no state", l.name)
	}
	var st session.LaneState
	if err := json.Unmarshal(raw, &st); err != nil {
		return session.LaneState{}, err
	}
	return st, nil
}

// GetTipID returns the lane branch's current tip entry id, or "", false at
// the root.
func (l *Lane) GetTipID() (string, bool) {
	raw, _, ok := l.h.opts.Storage.GetValue(session.NamespaceBranchTip, l.name)
	if !ok {
		return "", false
	}
	var tip *string
	if err := json.Unmarshal(raw, &tip); err != nil || tip == nil {
		return "", false
	}
	return *tip, true
}

// FindEntries returns every entry on the lane's branch, tip first
// (newest-first), as pi's own FindEntries does.
func (l *Lane) FindEntries(ctx context.Context) ([]session.Entry, error) {
	tip, ok := l.GetTipID()
	if !ok {
		return nil, nil
	}
	return l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "newestFirst"})
}

// SetActiveTools updates the lane's active tool set and writes it into
// pi.lane.config.
func (l *Lane) SetActiveTools(names []string) error {
	cfg, err := l.config()
	if err != nil {
		return err
	}
	cfg.ActiveToolNames = names
	w, err := session.SetValue(session.LaneConfig(l.name), cfg)
	if err != nil {
		return err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{w}); err != nil {
		return err
	}
	l.h.events.Emit(Event{Type: EventConfigUpdate, Lane: l.name, ConfigProperty: ConfigActiveTools})
	return nil
}

// GetActiveTools returns the lane's current active tool names.
func (l *Lane) GetActiveTools() ([]string, error) {
	cfg, err := l.config()
	if err != nil {
		return nil, err
	}
	return cfg.ActiveToolNames, nil
}

// SetThinkingLevel records the lane's thinking level, leaving its model as
// it is. A no-op when the level is already set to it.
func (l *Lane) SetThinkingLevel(level string) error {
	cfg, err := l.config()
	if err != nil {
		return err
	}
	if cfg.ThinkingLevel == level {
		return nil
	}
	cfg.ThinkingLevel = level
	w, err := session.SetValue(session.LaneConfig(l.name), cfg)
	if err != nil {
		return err
	}
	_, err = l.h.opts.Storage.Commit([]session.Write{w})
	return err
}

// SetModel updates the lane's model/thinking-level configuration.
func (l *Lane) SetModel(model session.ModelRef, thinkingLevel string) error {
	cfg, err := l.config()
	if err != nil {
		return err
	}
	cfg.Model = model
	if thinkingLevel != "" {
		cfg.ThinkingLevel = thinkingLevel
	}
	w, err := session.SetValue(session.LaneConfig(l.name), cfg)
	if err != nil {
		return err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{w}); err != nil {
		return err
	}
	l.h.events.Emit(Event{Type: EventConfigUpdate, Lane: l.name, ConfigProperty: ConfigModel})
	return nil
}

// Watch returns a snapshot of the lane's current branch (tip first) plus a
// channel that receives every entry newly committed to this lane's branch
// from this point on. Callers must drain or discard the channel; it is
// closed when unsubscribe is called.
func (l *Lane) Watch() (snapshot []session.Entry, updates <-chan session.Entry, unsubscribe func()) {
	snapshot, _ = l.FindEntries(context.Background())
	ch := make(chan session.Entry, 64)
	unsub := l.h.events.On(EventEntryAdded, func(ev Event) {
		if ev.Lane != l.name {
			return
		}
		entries := l.h.opts.Storage.GetEntries([]string{ev.EntryID})
		if e, ok := entries[ev.EntryID]; ok {
			select {
			case ch <- e:
			default:
			}
		}
	})
	return snapshot, ch, func() { unsub(); close(ch) }
}

// Abort cancels the lane's in-flight operation, if any. It commits a
// terminal pi.result with status "aborted" from the turn loop's own
// goroutine once it observes ctx.Err() != nil; Abort itself only requests
// cancellation and returns immediately.
func (l *Lane) Abort() error {
	l.mu.Lock()
	cancel := l.cancel
	l.mu.Unlock()
	if cancel == nil {
		return fmt.Errorf("harness: lane %q has no running operation to abort", l.name)
	}
	cancel()
	l.h.events.Emit(Event{Type: EventOperationAbort, Lane: l.name})
	return nil
}

// Steer queues text as the next thing the lane's run loop injects into the
// transcript, at the next checkpoint. This is a best-effort approximation
// of pi's steering (which can interrupt an in-flight generation
// mid-stream); this phase only supports queuing between turns, via
// pi.lane.state.inbox.
//
// Steer deliberately does NOT move the branch tip: the entry it commits is
// parented to nothing and sits off the branch until drive()'s inbox drain
// (turn.go's drainInbox) re-parents a copy of it onto the tip current at
// the next checkpoint. Writing straight to the tip here raced the running
// turn's own end-of-message tip write (turn.go's `SetValue(BranchTip(...),
// &responseEntryID)`) and always lost — whichever commit landed last threw
// the other's tip update away, so the queued entry silently fell off the
// branch and the next Prompt's transcript never saw it. Recording it in
// the inbox instead means the drain is the only thing that ever moves the
// tip on the queued entry's behalf, from the same goroutine that is
// already serializing every other tip write for this lane.
func (l *Lane) Steer(text string) error {
	st, err := l.laneState()
	if err != nil {
		return err
	}
	entryID := l.newID()
	entryWrite := session.EntryWrite{Entry: session.Entry{
		ID:      entryID,
		Type:    session.EntryMessage,
		Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(text)}, Timestamp: l.now()},
	}}
	st.Inbox = append(st.Inbox, session.InboxItem{EntryID: entryID, Kind: "steer"})
	w, err := session.SetValue(session.LaneStateValue(l.name), st)
	if err != nil {
		return err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{entryWrite, w}); err != nil {
		return err
	}
	l.h.events.Emit(Event{Type: EventQueueUpdate, Lane: l.name, QueueLen: len(st.Inbox)})
	return nil
}

// ClearInbox withdraws every follow-up queued via Steer that the run loop
// has not drained yet and returns their texts, oldest first. The entries
// stay in the log, off the branch, like any other undelivered steer. Used
// when the user interrupts a turn: the queued text goes back to them to
// edit or send, instead of riding along with whatever they say next.
func (l *Lane) ClearInbox() ([]string, error) {
	st, err := l.laneState()
	if err != nil {
		return nil, err
	}
	if len(st.Inbox) == 0 {
		return nil, nil
	}
	ids := make([]string, len(st.Inbox))
	for i, item := range st.Inbox {
		ids[i] = item.EntryID
	}
	entries := l.h.opts.Storage.GetEntries(ids)
	var texts []string
	for _, id := range ids {
		if e, ok := entries[id]; ok {
			if um, ok := e.Message.(msg.UserMessage); ok {
				texts = append(texts, msg.TextOf(um.Content))
			}
		}
	}
	st.Inbox = nil
	w, err := session.SetValue(session.LaneStateValue(l.name), st)
	if err != nil {
		return nil, err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{w}); err != nil {
		return nil, err
	}
	l.h.events.Emit(Event{Type: EventQueueUpdate, Lane: l.name, QueueLen: 0})
	return texts, nil
}

// NavigateTree moves the lane's branch tip to targetID (nil for the root).
// It refuses while an operation is running: pi's navigation.ready_to_commit
// non-run path (rewriting history under a live operation) is not
// implemented.
func (l *Lane) NavigateTree(ctx context.Context, targetID *string) error {
	st, err := l.laneState()
	if err != nil {
		return err
	}
	if st.CurrentOperationID != nil {
		return fmt.Errorf("harness: lane %q: cannot navigate while an operation is running", l.name)
	}
	for _, fn := range l.h.hooks.beforeNavigation {
		if fn == nil {
			continue
		}
		if err := runGuarded(func() error { return fn(ctx, targetID) }); err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_navigation", Err: err})
		}
	}
	l.h.events.Emit(Event{Type: EventNavigationStart, Lane: l.name})
	w, err := session.SetValue(session.BranchTip(l.name), targetID)
	if err != nil {
		return err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{w}); err != nil {
		return err
	}
	l.h.events.Emit(Event{Type: EventNavigationEnd, Lane: l.name})
	return nil
}

// Compact runs an out-of-band compaction now, via internal/compaction,
// using the lane's own configured provider/model. custom, if non-nil, is
// passed through as compaction.Compact's customInstructions (steering the
// summarizer's prompt, matching pi's /compact <text>; it does not replace
// the summary outright).
func (l *Lane) Compact(ctx context.Context, custom *string) error {
	_, cfg, err := l.resolveModel()
	if err != nil {
		return err
	}
	model, ok := l.h.opts.Registry.GetModel(cfg.Model.Provider, cfg.Model.ModelID)
	if !ok {
		return fmt.Errorf("harness: unknown model %s/%s", cfg.Model.Provider, cfg.Model.ModelID)
	}
	tip, _ := l.GetTipID()
	if tip == "" {
		return fmt.Errorf("harness: lane %q: nothing to compact", l.name)
	}
	pathEntries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "oldestFirst"})
	if err != nil {
		return err
	}
	return l.runCompaction(ctx, pathEntries, model, cfg, custom)
}

// EstimateConversationTokens estimates the tokens of the conversation the
// model is sent next (after any compaction), not counting the system
// prompt or tools. An estimate (compaction.EstimateTokens), for reports.
func (l *Lane) EstimateConversationTokens() (int, error) {
	tip, _ := l.GetTipID()
	if tip == "" {
		return 0, nil
	}
	entries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "oldestFirst"})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, m := range entriesToTranscript(entries) {
		total += compaction.EstimateTokens(m)
	}
	return total, nil
}

// entriesToTranscript projects entries (already oldest-first, as
// ScanBranch with Order "oldestFirst" returns them) onto the messages the
// model sees. It is compaction-aware: history before the last compaction
// entry is replaced by that entry's summary and retained tail
// (compaction.ContextMessages), which is what makes compaction shrink the
// next request rather than only the session's bookkeeping.
func entriesToTranscript(entries []session.Entry) []msg.Message {
	return compaction.ContextMessages(entries)
}

// resolveModel resolves the lane's configured provider and model.
func (l *Lane) resolveModel() (provider.Model, session.LaneConfiguration, error) {
	cfg, err := l.config()
	if err != nil {
		return provider.Model{}, cfg, err
	}
	model, ok := l.h.opts.Registry.GetModel(cfg.Model.Provider, cfg.Model.ModelID)
	if !ok {
		return provider.Model{}, cfg, fmt.Errorf("harness: unknown model %s/%s", cfg.Model.Provider, cfg.Model.ModelID)
	}
	if _, ok := l.h.opts.Registry.Provider(cfg.Model.Provider); !ok {
		return provider.Model{}, cfg, fmt.Errorf("harness: unknown provider %s", cfg.Model.Provider)
	}
	return model, cfg, nil
}

// sleepCtx sleeps for d, or returns early if ctx is cancelled or retryNow
// fires (nil retryNow behaves as before: no early-retry path). retryNow
// firing is not an error — the caller reads it the same as the delay having
// simply elapsed, and proceeds straight to the next attempt.
func sleepCtx(ctx context.Context, d time.Duration, retryNow <-chan struct{}) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-retryNow:
		return nil
	}
}

// RetryNow cuts a pending retry backoff short: the next sleepCtx call inside
// the lane's retry loop (if any is currently waiting) returns immediately
// instead of waiting out the rest of its delay. Non-blocking and safe to
// call when no retry is pending — the signal is simply buffered (and
// harmlessly drained, unused, by the next retry's sleepCtx call) or dropped
// if the buffer is already full.
func (l *Lane) RetryNow() {
	l.mu.Lock()
	ch := l.retryNow
	l.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// ToolSchemaTokens estimates the tokens this lane's active tool schemas
// occupy in every request it sends. It serializes exactly the
// provider.ToolDef list buildStreamOptions builds (name, description and
// JSON Schema parameters, in the lane's own active order) and applies the
// same chars/4 heuristic the rest of the context accounting uses.
//
// This exists so /context can report a measured Tools segment instead of
// budget.ToolStrategyCost's fixed per-strategy estimate, which is a
// planning ceiling and can be far from what a given session actually
// sends — an overshoot there consumed the whole measured context and drove
// the Conversation segment to zero.
func (l *Lane) ToolSchemaTokens() (int, error) {
	cfg, err := l.config()
	if err != nil {
		return 0, err
	}
	tools := l.h.toolSet()
	if tools == nil {
		return 0, nil
	}
	var defs []provider.ToolDef
	for _, name := range cfg.ActiveToolNames {
		t, ok := tools.Get(name)
		if !ok {
			continue
		}
		defs = append(defs, provider.ToolDef{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	if len(defs) == 0 {
		return 0, nil
	}
	encoded, err := json.Marshal(defs)
	if err != nil {
		return 0, err
	}
	return (len(encoded) + 3) / 4, nil
}
