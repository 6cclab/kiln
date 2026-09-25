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
func (l *Lane) Steer(text string) error {
	st, err := l.laneState()
	if err != nil {
		return err
	}
	entryID := l.newID()
	entryWrite := session.EntryWrite{Entry: session.Entry{
		ParentID: nil, // resolved to current tip by commitEntry below
		Type:     session.EntryMessage,
		Message:  msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(text)}, Timestamp: l.now()},
	}}
	_ = entryID
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

// sleepCtx sleeps for d or returns early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
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
	}
}
