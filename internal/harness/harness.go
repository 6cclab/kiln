package harness

import (
	"fmt"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tool"
)

// Options configures a new Harness.
type Options struct {
	Storage  session.Storage
	Registry *provider.Registry

	Model         session.ModelRef
	ThinkingLevel string

	Tools           *tool.Set
	ActiveToolNames []string

	SystemPrompt string

	// Compaction is compaction.Settings (Enabled, ReserveTokens,
	// KeepRecentTokens). The model called to summarize is always the
	// lane's own provider/model (via Registry); there is no separate
	// compaction-only provider.
	Compaction CompactionSettings

	Retry RetryPolicy

	Cwd string

	// Now, if set, replaces time.Now for entry/operation timestamps
	// (tests only).
	Now func() time.Time
}

// Harness is the runtime that drives one or more Lanes over one
// session.Storage. It owns the Events bus and the Hooks registry that every
// Lane it creates shares.
type Harness struct {
	opts   Options
	events *Events
	hooks  *Hooks

	mu    sync.Mutex
	lanes map[string]*Lane
}

// New validates opts and returns a ready Harness. It does not create any
// lane; call Lane to create or fetch one.
func New(opts Options) (*Harness, error) {
	if opts.Storage == nil {
		return nil, fmt.Errorf("harness: Options.Storage is required")
	}
	if opts.Registry == nil {
		return nil, fmt.Errorf("harness: Options.Registry is required")
	}
	if opts.Tools == nil {
		opts.Tools = tool.NewSet()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.Retry = opts.Retry.normalized()
	if opts.ThinkingLevel == "" {
		opts.ThinkingLevel = "off"
	}
	return &Harness{
		opts:   opts,
		events: NewEvents(),
		hooks:  NewHooks(),
		lanes:  map[string]*Lane{},
	}, nil
}

// Events returns the harness's event bus.
func (h *Harness) Events() *Events { return h.events }

// Hooks returns the harness's hook registry.
func (h *Harness) Hooks() *Hooks { return h.hooks }

// SetTools replaces the tool set every lane resolves ActiveToolNames
// against. It does not, by itself, change any lane's ActiveToolNames.
func (h *Harness) SetTools(tools *tool.Set) {
	h.mu.Lock()
	h.opts.Tools = tools
	h.mu.Unlock()
	h.events.Emit(Event{Type: EventConfigUpdate, ConfigProperty: ConfigTools})
}

// SetSystemPrompt replaces the system prompt the next request is built
// with. Used when the MCP tool index arrives after the session started.
func (h *Harness) SetSystemPrompt(prompt string) {
	h.mu.Lock()
	h.opts.SystemPrompt = prompt
	h.mu.Unlock()
	h.events.Emit(Event{Type: EventConfigUpdate, ConfigProperty: ConfigSystemPrompt})
}

// AddTools registers tools alongside the existing ones, replacing any with
// the same name, without activating them. The set is rebuilt rather than
// mutated so a turn already holding the old set keeps a consistent view.
func (h *Harness) AddTools(tools ...*tool.Tool) {
	h.mu.Lock()
	old := h.opts.Tools
	next := tool.NewSet()
	if old != nil {
		for _, name := range old.Names() {
			if t, ok := old.Get(name); ok {
				next.Add(t)
			}
		}
	}
	for _, t := range tools {
		next.Add(t)
	}
	h.opts.Tools = next
	h.mu.Unlock()
	h.events.Emit(Event{Type: EventConfigUpdate, ConfigProperty: ConfigTools})
}

// SetCompactionSettings replaces the default compaction settings new
// operations pick up; it does not affect an operation already running.
func (h *Harness) SetCompactionSettings(s CompactionSettings) {
	h.mu.Lock()
	h.opts.Compaction = s
	h.mu.Unlock()
	h.events.Emit(Event{Type: EventConfigUpdate, ConfigProperty: ConfigCompactionSettings})
}

// SetName sets the session's display name (pi.session.name) and emits a
// value_update event.
func (h *Harness) SetName(name string) error {
	w, err := session.SetValue(session.SessionName(), name)
	if err != nil {
		return err
	}
	if _, err := h.opts.Storage.Commit([]session.Write{w}); err != nil {
		return err
	}
	h.events.Emit(Event{Type: EventValueUpdate, ValueNamespace: ValueSessionName})
	return nil
}

// Close closes the underlying storage.
func (h *Harness) Close() error { return h.opts.Storage.Close() }

// Lane returns the named lane, creating it (and writing its initial
// pi.branch.tip / pi.lane.config / pi.lane.state values) if this is the
// first time it has been asked for. These writes used to happen in
// jsonl.Repo.Create; this phase moves them here so a session can host more
// than one lane and so a lane's initial configuration reflects the
// Harness's Options rather than a fixed default (see repo.go item 6 in the
// phase brief).
func (h *Harness) Lane(name string) (*Lane, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l, ok := h.lanes[name]; ok {
		return l, nil
	}
	if _, _, ok := h.opts.Storage.GetValue(session.NamespaceLaneConfig, name); !ok {
		writes, err := initialLaneWrites(name, h.opts.Model, h.opts.ThinkingLevel, h.opts.ActiveToolNames)
		if err != nil {
			return nil, err
		}
		if _, err := h.opts.Storage.Commit(writes); err != nil {
			return nil, fmt.Errorf("harness: failed to create lane %q: %w", name, err)
		}
	}
	l := &Lane{h: h, name: name}
	h.lanes[name] = l
	h.events.Emit(Event{Type: EventLaneCreated, Lane: name})
	return l, nil
}

// initialLaneWrites is the transaction a lane's creation writes: an empty
// branch tip, its lane configuration and its idle lane state. This mirrors
// pi's repo.create writes, now issued by the harness instead of the
// storage layer.
func initialLaneWrites(lane string, model session.ModelRef, thinkingLevel string, activeTools []string) ([]session.Write, error) {
	tip, err := session.SetValue(session.BranchTip(lane), (*string)(nil))
	if err != nil {
		return nil, err
	}
	if activeTools == nil {
		activeTools = []string{}
	}
	cfg, err := session.SetValue(session.LaneConfig(lane), session.LaneConfiguration{
		Model:           model,
		ThinkingLevel:   thinkingLevel,
		ActiveToolNames: activeTools,
	})
	if err != nil {
		return nil, err
	}
	st, err := session.SetValue(session.LaneStateValue(lane), session.LaneState{Inbox: []session.InboxItem{}})
	if err != nil {
		return nil, err
	}
	return []session.Write{tip, cfg, st}, nil
}
