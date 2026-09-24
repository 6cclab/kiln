// Package tui holds the terminal UI: the app model, editor, and the global
// key router.
package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// DoublePress is how long a second press of ctrl+c or esc still counts as
// the second half of a double press. Matches keys.ts's DOUBLE_PRESS_MS.
const DoublePress = 1000 * time.Millisecond

// KeyActions are the effects the router triggers. Ported field-for-field
// from keys.ts:57-74 (KeyActions there), as function fields rather than an
// interface so a test can wire only the ones a case cares about.
type KeyActions struct {
	// PermissionKey reports whether a permission prompt is up and took the
	// key. Nil means no prompt is active. Checked before anything else: a
	// prompt owns the keyboard while it is up, and Esc there means "decline
	// this call", not "abort the turn".
	PermissionKey func(msg tea.KeyPressMsg) bool

	// IsBusy reports whether a turn is running.
	IsBusy func() bool
	// HasInput reports whether the input line currently has text.
	HasInput func() bool

	Interrupt           func()
	ToggleExpanded      func()
	CyclePermissionMode func()
	ClearInput          func()
	ClearScreen         func()
	Rewind              func()
	Exit                func()
	// Hint surfaces a transient message for the footer, e.g. "press ctrl+c
	// again to exit".
	Hint func(message string)

	// Now is injected so double-press timing is testable without waiting.
	// Defaults to time.Now when left nil.
	Now func() time.Time
}

// Router is the global key router (parity spec §6, keys.ts:83-158). Ordering
// is the whole design here: each binding is checked before the editor sees
// the key, and the order encodes what a key means right now.
//
//   - A permission prompt owns the keyboard while it is up.
//   - Esc during a running turn means interrupt. Only when idle does a
//     second Esc mean rewind.
//   - Ctrl+C clears the input; a second one within the window exits. One
//     press exiting would make a mistyped line cost the session.
//
// Unlike the TS router, this one does not need to worry about wire
// encodings: bubbletea's Kitty/legacy/modifyOtherKeys disambiguation has
// already happened by the time a tea.KeyPressMsg reaches Route, and
// msg.String() gives one canonical label ("shift+tab", "ctrl+c", "esc", ...)
// regardless of which encoding the terminal used. Comparing bytes was the
// bug in keys.ts; there is no byte layer here to compare.
type Router struct {
	actions   KeyActions
	lastCtrlC time.Time
	lastEsc   time.Time
}

// NewRouter builds a Router. If actions.Now is nil, time.Now is used.
func NewRouter(actions KeyActions) *Router {
	if actions.Now == nil {
		actions.Now = time.Now
	}
	return &Router{actions: actions}
}

// Route applies the global key bindings to a key press, in the exact
// precedence of keys.ts:88-157. It returns true when the key was consumed
// (the editor and every other handler must not also act on it) and false
// when the key falls through to whatever handles it next (usually the
// editor).
func (r *Router) Route(msg tea.KeyPressMsg) bool {
	// The prompt owns the keyboard while it is up, ahead of everything else.
	if r.actions.PermissionKey != nil && r.actions.PermissionKey(msg) {
		return true
	}

	switch msg.String() {
	case "ctrl+r":
		r.actions.ToggleExpanded()
		return true

	case "shift+tab":
		r.actions.CyclePermissionMode()
		return true

	case "ctrl+l":
		r.actions.ClearScreen()
		return true

	case "ctrl+c":
		now := r.actions.Now()
		if now.Sub(r.lastCtrlC) < DoublePress {
			r.actions.Exit()
			return true
		}
		r.lastCtrlC = now
		// Interrupting counts as the useful thing to do first; the second
		// press still exits.
		if r.actions.IsBusy() {
			r.actions.Interrupt()
		} else {
			r.actions.ClearInput()
		}
		r.actions.Hint("press ctrl+c again to exit")
		return true

	case "ctrl+d":
		// Only on an empty line: on a line with text, Ctrl+D is a
		// delete-forward the editor should handle, and exiting instead
		// would lose what was typed.
		if r.actions.HasInput() {
			return false
		}
		r.actions.Exit()
		return true

	case "esc":
		if r.actions.IsBusy() {
			r.actions.Interrupt()
			// Consumed: while a turn runs Esc means interrupt, not "clear
			// the input line". Does not arm the double-press: an interrupt
			// followed by a single Esc must not silently rewind.
			return true
		}
		now := r.actions.Now()
		if now.Sub(r.lastEsc) < DoublePress {
			r.lastEsc = time.Time{}
			r.actions.Rewind()
			return true
		}
		r.lastEsc = now
		// Not consumed: a single Esc when idle still belongs to the editor.
		return false
	}

	return false
}

// RouteRelease handles a key release. It is a no-op: bubbletea v2 only
// reports key releases when the program calls tea.WithReportEventTypes (or
// enables the Kitty keyboard protocol's event-type reporting some other
// way), and this app never does. Acting on a release here would be dead
// code today, but the method exists so a caller that dispatches on
// tea.KeyMsg does not need a special case for the type it must ignore.
func (r *Router) RouteRelease(_ tea.KeyReleaseMsg) {}
