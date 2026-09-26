package tui

import "sync"

// The "live while last" rule (docs/kiln-design-handoff/README.md's plan/
// subagents blocks: "Blocks update in place"): the plan checklist and the
// subagents panel render in the live region — redrawn every frame, below
// the committed transcript — only while nothing else has committed since
// they last changed. The instant anything else commits (a tool call, a
// diff, assistant text, a note, a user echo…), whatever is still live
// freezes: it commits to the transcript, in order, immediately above that
// new block, and stops rendering live until a later update (a fresh
// todo_write, a fresh subagent event) makes it live again.
//
// The mechanism: planLiveState (below) and SubagentPanelState
// (subagents.go) each hold their own mutex and a `frozen` bool, so they can
// be read/frozen safely from any goroutine — not just Update's. Bridge's
// commit path calls a hook (SetFreezeHook) that snapshots and freezes
// whichever of the two is still live, and commits the result ahead of
// whatever the caller is about to commit; app.go wires that hook, in
// NewModel, to the two live pointers it already owns (m.plan,
// m.subagents — both stable pointers shared across every Model value the
// same way m.subagents already was before this pass).
//
// Two commit paths exist:
//   - Bridge.FreezeBefore(), called by app.go's m.commit/m.commitSynthetic/
//     m.commitNote helpers (and by bridge.go's own event-driven commits:
//     EventFault, SubagentSink, HookNotice, ModelSwitch) ahead of an
//     ordinary transcript commit — this is what makes freezing automatic
//     for the common case.
//   - finishTurn's own explicit plan.freeze/subagents.Freeze calls, which
//     guarantee the turn's live state lands in the transcript even when no
//     further commit happens to trigger the hook (e.g. a todo_write with
//     no subsequent tool call or text before the turn ends). freeze/Freeze
//     are idempotent (a second call once already frozen returns nil), so
//     there is no risk of double-committing whichever one the automatic
//     hook already caught.
//   - finishTurn's abort path (InFlightTools' tool-error blocks) is the one
//     deliberate exception: those commits go through Bridge.Commit
//     directly, bypassing the freeze helpers, so the interrupted tool
//     blocks land before the plan/subagents freeze that follows them —
//     matching the design's required order (tool errors, then plan, then
//     subagents, then the "Interrupted" note).

// planLiveState holds the plan checklist's live/frozen state. See this
// file's doc comment for the mechanism; mirrors SubagentPanelState's own
// mu+frozen shape in subagents.go.
type planLiveState struct {
	mu     sync.Mutex
	items  []TodoView
	frozen bool
}

// Set replaces the plan's items (MsgTodos) and makes it live again.
func (p *planLiveState) Set(items []TodoView) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items = items
	p.frozen = false
}

// Reset clears the plan, called at the start of every turn (beginTurn) and
// once finishTurn has committed its final state.
func (p *planLiveState) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items = nil
	p.frozen = false
}

// Live returns the items to render in the live region, or nil while frozen
// or empty.
func (p *planLiveState) Live() []TodoView {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.frozen || len(p.items) == 0 {
		return nil
	}
	return p.items
}

// freeze marks the plan frozen and returns its current state as
// commit-ready lines (a leading blank row, matching finishTurn's previous
// literal `append([]string{""}, ...)`), or nil if already frozen or empty.
func (p *planLiveState) freeze(width int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.frozen || len(p.items) == 0 {
		return nil
	}
	p.frozen = true
	return append([]string{""}, RenderPlan(p.items, width)...)
}

// newFreezeHook builds the closure Bridge.SetFreezeHook installs: it
// freezes, in order, whatever of plan/subagents is still live, at the
// given width, and returns the combined commit-ready lines (nil if
// neither is live). Factored out of NewModel so it has no dependency on
// Model itself — only the two shared pointers and a width reader, all of
// which are safe to call from any goroutine.
func newFreezeHook(plan *planLiveState, subagents *SubagentPanelState, width func() int) func() []string {
	return func() []string {
		w := width()
		if w <= 0 {
			w = 80
		}
		var out []string
		if lines := plan.freeze(w); len(lines) > 0 {
			out = append(out, lines...)
		}
		if lines := subagents.Freeze(w); len(lines) > 0 {
			out = append(out, lines...)
		}
		return out
	}
}
