package harness

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/router"
	"github.com/andrepato/harness/internal/session"
)

// RouterStateEntry is the CustomType of the session entry that stores a
// virtual model's router state on the branch (pi's
// "pi.virtual-model-state"). Its Data is a RouterStateData.
const RouterStateEntry = "kiln.router.state"

// RouterStateData is a RouterStateEntry's Data: the state a virtual
// model's router last returned on this branch.
type RouterStateData struct {
	Provider string          `json:"provider"`
	ModelID  string          `json:"modelId"`
	State    json.RawMessage `json:"state"`
}

// routed is the model one request goes to. On a physical lane it is the
// lane's own model; on a virtual lane, what the router chose.
type routed struct {
	Model provider.Model
	// Thinking is the level the request is sent at; "" lets the model
	// decide.
	Thinking string
	// virtual is the lane's virtual model, nil on a physical lane.
	virtual *provider.Model
	// state is the router state this route returned, to be stored on the
	// branch when unsaved is set (it differs from the branch's).
	state   json.RawMessage
	unsaved bool
}

// streamConfig is cfg with the routed thinking level, for
// buildStreamOptions. On a physical lane it is the level cfg and the
// harness options already gave.
func (r routed) streamConfig(cfg session.LaneConfiguration) session.LaneConfiguration {
	cfg.ThinkingLevel = r.Thinking
	return cfg
}

// failure is r's request failing with err, for the retry that follows.
func (r routed) failure(err error) *router.Failure {
	return &router.Failure{Model: r.Model, ThinkingLevel: provider.ThinkingLevel(r.Thinking), Err: err}
}

// route picks the model of the lane's next request. A physical lane gets
// its configured model unchanged, without reading the branch. A virtual
// lane asks its router (Options.Routers) with reason, transcript and, for
// a retry, the failed request; the router reads the state stored on the
// branch at tip, or carried's when carried holds state not yet stored. A
// direct request neither reads nor writes state.
//
// A nil transcript is the conversation on the branch at tip, and an empty
// reason is a turn-loop request's (requestReason).
func (l *Lane) route(ctx context.Context, reason router.Reason, tip string, transcript []msg.Message, failed *router.Failure, carried *routed) (routed, session.LaneConfiguration, error) {
	model, cfg, err := l.resolveModel()
	if err != nil {
		return routed{}, cfg, err
	}
	thinking := cfg.ThinkingLevel
	if thinking == "" {
		thinking = l.h.opts.ThinkingLevel
	}
	if !provider.IsVirtual(model) {
		return routed{Model: model, Thinking: thinking}, cfg, nil
	}
	if transcript == nil && tip != "" {
		entries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "oldestFirst"})
		if err != nil {
			return routed{}, cfg, err
		}
		transcript = entriesToTranscript(entries)
	}
	if reason == "" {
		reason = requestReason(transcript)
	}
	var state json.RawMessage
	if reason != router.ReasonDirect {
		if carried != nil && carried.unsaved {
			state = carried.state
		} else {
			state = l.routerState(tip, model)
		}
	}
	route, err := l.h.opts.Routers.Resolve(ctx, l.h.opts.Registry, router.Request{
		Virtual:       model,
		ThinkingLevel: provider.ThinkingLevel(thinking),
		Reason:        reason,
		Failed:        failed,
		State:         state,
		Messages:      transcript,
	})
	if err != nil {
		return routed{}, cfg, err
	}
	virtual := model
	out := routed{Model: route.Model, Thinking: string(route.ThinkingLevel), virtual: &virtual, state: state}
	if route.State != nil && !bytes.Equal(route.State, state) {
		out.state, out.unsaved = route.State, true
	}
	if reason != router.ReasonDirect {
		l.mu.Lock()
		l.lastRouted = &session.ModelRef{Provider: route.Model.Provider, ModelID: route.Model.ID}
		l.mu.Unlock()
	}
	l.h.events.Emit(Event{Type: EventRoute, Lane: l.name, RouteReason: string(reason),
		RouteVirtual:       session.ModelRef{Provider: model.Provider, ModelID: model.ID},
		RouteModel:         session.ModelRef{Provider: route.Model.Provider, ModelID: route.Model.ID},
		RouteThinkingLevel: out.Thinking})
	return out, cfg, nil
}

// routerState is the state virtual's router last stored on the branch at
// tip: the newest RouterStateEntry for that model, nil when there is none.
// The walk follows the branch, so state follows /rewind and forks.
func (l *Lane) routerState(tip string, virtual provider.Model) json.RawMessage {
	if tip == "" {
		return nil
	}
	entries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "newestFirst"})
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Type != session.EntryCustom || e.CustomType != RouterStateEntry {
			continue
		}
		var d RouterStateData
		if json.Unmarshal(e.Data, &d) == nil && d.Provider == virtual.Provider && d.ModelID == virtual.ID {
			return d.State
		}
	}
	return nil
}

// stateWrites are the writes that store r's unsaved router state on the
// branch after tip: the state entry and, when moveTip is set, the tip
// moved onto it. It returns the state entry's id, which is tip itself when
// there is nothing to store. The caller commits the writes with its own,
// so the state lands with the request it decided.
func (l *Lane) stateWrites(tip string, r *routed, moveTip bool) (string, []session.Write, error) {
	if r.virtual == nil || !r.unsaved {
		return tip, nil, nil
	}
	data, err := json.Marshal(RouterStateData{Provider: r.virtual.Provider, ModelID: r.virtual.ID, State: r.state})
	if err != nil {
		return tip, nil, err
	}
	id := l.newID()
	entry := session.Entry{ID: id, Type: session.EntryCustom, CustomType: RouterStateEntry, Data: data}
	if tip != "" {
		p := tip
		entry.ParentID = &p
	}
	writes := []session.Write{session.EntryWrite{Entry: entry}}
	if moveTip {
		tipW, err := session.SetValue(session.BranchTip(l.name), &id)
		if err != nil {
			return tip, nil, err
		}
		writes = append(writes, tipW)
	}
	r.unsaved = false
	return id, writes, nil
}

// requestReason is a turn-loop request's reason: user when a message the
// user wrote follows the last assistant message, continuation otherwise.
// A hook's message (a Stop hook keeping the turn going) is not the user's.
func requestReason(transcript []msg.Message) router.Reason {
	for i := len(transcript) - 1; i >= 0; i-- {
		switch m := transcript[i].(type) {
		case msg.AssistantMessage:
			return router.ReasonContinuation
		case msg.UserMessage:
			if m.KilnHook == "" {
				return router.ReasonUser
			}
		}
	}
	return router.ReasonContinuation
}

// summariser is the model that writes a compaction summary for a lane
// whose requests go to target: target itself on a physical lane, the
// router's direct route on a virtual one.
func (l *Lane) summariser(ctx context.Context, target routed, transcript []msg.Message) (provider.Model, error) {
	if target.virtual == nil {
		return target.Model, nil
	}
	direct, _, err := l.route(ctx, router.ReasonDirect, "", transcript, nil, nil)
	if err != nil {
		return provider.Model{}, err
	}
	return direct.Model, nil
}
