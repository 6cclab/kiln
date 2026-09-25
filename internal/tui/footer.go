package tui

import (
	"os"
	"time"
)

// FooterState is the stateful wrapper around StatusState/RenderStatus,
// ported from app.ts's FooterView (app.ts:410-448). RenderStatus itself is
// pure (status.go); this type holds the mutable pieces app.ts's FooterView
// carried on the instance: the underlying StatusState, a transient note,
// and the busy flag that appends the "esc to interrupt" hint.
type FooterState struct {
	state StatusState
	// note is a transient message appended to the first row, e.g.
	// "expanded", cleared by the next Update call, matching setNote's own
	// "shown until the next update clears it" contract.
	note string
	// busy appends "esc to interrupt" to the first row while a turn runs.
	busy bool
}

// NewFooterState builds a FooterState from an initial StatusState.
func NewFooterState(state StatusState) *FooterState {
	return &FooterState{state: state}
}

// Update patches fields of the underlying StatusState, leaving anything
// not set (via patch) unchanged. patch is applied field by field so a
// caller can update just one thing (e.g. only ContextUsed) without
// clobbering the rest.
type StatusPatch struct {
	ModelLabel    *string
	ContextWindow *int
	ContextUsed   *int
	Cost          *float64
	Git           *GitStatus
	Mode          *string
	Thinking      *bool
}

// Apply patches the state.
func (f *FooterState) Apply(p StatusPatch) {
	if p.ModelLabel != nil {
		f.state.ModelLabel = *p.ModelLabel
	}
	if p.ContextWindow != nil {
		f.state.ContextWindow = *p.ContextWindow
	}
	if p.ContextUsed != nil {
		f.state.ContextUsed = p.ContextUsed
	}
	if p.Cost != nil {
		f.state.Cost = *p.Cost
	}
	if p.Git != nil {
		f.state.Git = p.Git
	}
	if p.Mode != nil {
		f.state.Mode = *p.Mode
	}
	if p.Thinking != nil {
		f.state.Thinking = *p.Thinking
	}
}

// SetNote shows a transient note until the next Update, matching
// FooterView.setNote.
func (f *FooterState) SetNote(note string) { f.note = note }

// Note returns the transient note, "" when none.
func (f *FooterState) Note() string { return f.note }

// SetBusy toggles the "esc to interrupt" hint, matching FooterView.setBusy.
func (f *FooterState) SetBusy(busy bool) { f.busy = busy }

// State returns a copy of the current StatusState, e.g. to read Mode.
func (f *FooterState) State() StatusState { return f.state }

// clockOverride is read once from HARNESS_TEST_CLOCK (RFC3339) so PTY
// goldens do not depend on wall-clock time. Empty means "use time.Now".
// Read lazily (not at package init) so a test that sets the env var after
// the package loads still takes effect.
func clockOverride() (time.Time, bool) {
	v := os.Getenv("HARNESS_TEST_CLOCK")
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Render renders the footer's two status rows, then appends the transient
// hint / "esc to interrupt" suffix to the first row exactly as
// FooterView.render (app.ts:437-447) does, and fits both rows to width.
func (f *FooterState) Render(width int) [2]string {
	s := f.state
	if t, ok := clockOverride(); ok {
		s.Now = t
	}
	rows := RenderStatus(s)

	var bits []string
	if f.busy {
		bits = append(bits, "esc to interrupt")
	}
	if f.note != "" {
		bits = append(bits, f.note)
	}
	first := rows[0]
	if len(bits) > 0 {
		suffix := Dim("  ·  ")
		for i, b := range bits {
			if i > 0 {
				suffix += Dim(" · ")
			}
			suffix += Dim(b)
		}
		first = first + suffix
	}
	return [2]string{FitStatus(first, width), FitStatus(rows[1], width)}
}
