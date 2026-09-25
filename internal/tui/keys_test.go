package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// recorder captures which actions fired, mirroring test/keys.test.ts's
// Recorder.
type recorder struct {
	calls []string
	busy  bool
	input bool
	perm  func(tea.KeyPressMsg) bool
}

func (r *recorder) record(name string) func() {
	return func() { r.calls = append(r.calls, name) }
}

func (r *recorder) has(name string) bool {
	for _, c := range r.calls {
		if c == name {
			return true
		}
	}
	return false
}

// routed builds a Router with a clock the test controls, so timing needs no
// waiting (test/keys.test.ts's `routed`).
func routed() (*recorder, *Router, *time.Time) {
	r := &recorder{}
	clock := time.UnixMilli(10_000)
	actions := KeyActions{
		PermissionKey:       func(msg tea.KeyPressMsg) bool { return r.perm != nil && r.perm(msg) },
		IsBusy:              func() bool { return r.busy },
		HasInput:            func() bool { return r.input },
		Interrupt:           r.record("interrupt"),
		ToggleExpanded:      r.record("toggleExpanded"),
		CyclePermissionMode: r.record("cyclePermissionMode"),
		ClearInput:          r.record("clearInput"),
		ClearScreen:         r.record("clearScreen"),
		Rewind:              r.record("rewind"),
		Exit:                r.record("exit"),
		Hint:                func(string) { r.calls = append(r.calls, "hint") },
		Now:                 func() time.Time { return clock },
	}
	router := NewRouter(actions)
	return r, router, &clock
}

func advance(clock *time.Time, d time.Duration) {
	*clock = clock.Add(d)
}

// --- String() sanity: confirm the exact labels bubbletea produces for the
// keys the router matches on, before trusting the router to match them. ---

func TestKeyStrings(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyPressMsg
		want string
	}{
		{"ctrl+r", tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}, "ctrl+r"},
		{"shift+tab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, "shift+tab"},
		{"ctrl+l", tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}, "ctrl+l"},
		{"ctrl+c", tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, "ctrl+c"},
		{"ctrl+d", tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, "ctrl+d"},
		{"esc", tea.KeyPressMsg{Code: tea.KeyEscape}, "esc"},
		{"enter", tea.KeyPressMsg{Code: tea.KeyEnter}, "enter"},
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}, "shift+enter"},
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}, "alt+enter"},
		{"up", tea.KeyPressMsg{Code: tea.KeyUp}, "up"},
		{"down", tea.KeyPressMsg{Code: tea.KeyDown}, "down"},
		{"tab", tea.KeyPressMsg{Code: tea.KeyTab}, "tab"},
		{"a", tea.KeyPressMsg{Code: 'a', Text: "a"}, "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.msg.String(); got != c.want {
				t.Fatalf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func shiftTab() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift} }
func ctrlL() tea.KeyPressMsg    { return tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl} }
func ctrlC() tea.KeyPressMsg    { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }
func ctrlD() tea.KeyPressMsg    { return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl} }
func esc() tea.KeyPressMsg      { return tea.KeyPressMsg{Code: tea.KeyEscape} }

func ctrlO() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl} }

func TestTogglesVerboseOnCtrlO(t *testing.T) {
	r, router, _ := routed()
	if !router.Route(ctrlO()) {
		t.Fatal("not consumed")
	}
	if len(r.calls) != 1 || r.calls[0] != "toggleExpanded" {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestCyclesPermissionModeOnShiftTab(t *testing.T) {
	r, router, _ := routed()
	if !router.Route(shiftTab()) {
		t.Fatal("not consumed")
	}
	if len(r.calls) != 1 || r.calls[0] != "cyclePermissionMode" {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestClearsScreenOnCtrlL(t *testing.T) {
	r, router, _ := routed()
	router.Route(ctrlL())
	if !r.has("clearScreen") {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestPermissionPromptTakesTheKeyBeforeAnythingElse(t *testing.T) {
	// Esc during a prompt means "decline this call", not "abort the turn".
	r, router, _ := routed()
	r.perm = func(tea.KeyPressMsg) bool { return true }
	r.busy = true
	if !router.Route(esc()) {
		t.Fatal("not consumed")
	}
	if r.has("interrupt") {
		t.Fatal("the turn was aborted from inside a prompt")
	}
}

func TestEsc(t *testing.T) {
	t.Run("interrupts a running turn", func(t *testing.T) {
		r, router, _ := routed()
		r.busy = true
		if !router.Route(esc()) {
			t.Fatal("not consumed")
		}
		if len(r.calls) != 1 || r.calls[0] != "interrupt" {
			t.Fatalf("calls = %v", r.calls)
		}
	})

	t.Run("is left to the editor on a single press when idle", func(t *testing.T) {
		r, router, _ := routed()
		if router.Route(esc()) {
			t.Fatal("a single idle Esc was consumed")
		}
		if len(r.calls) != 0 {
			t.Fatalf("calls = %v", r.calls)
		}
	})

	t.Run("rewinds on a double press when idle", func(t *testing.T) {
		r, router, _ := routed()
		router.Route(esc())
		if !router.Route(esc()) {
			t.Fatal("not consumed")
		}
		if len(r.calls) != 1 || r.calls[0] != "rewind" {
			t.Fatalf("calls = %v", r.calls)
		}
	})

	t.Run("does not rewind when the presses are far apart", func(t *testing.T) {
		r, router, clock := routed()
		router.Route(esc())
		advance(clock, DoublePress+time.Millisecond)
		router.Route(esc())
		if len(r.calls) != 0 {
			t.Fatalf("calls = %v", r.calls)
		}
	})

	t.Run("does not treat interrupt-then-Esc as a double press", func(t *testing.T) {
		// Interrupting a turn and then pressing Esc once should not
		// silently rewind the conversation.
		r, router, _ := routed()
		r.busy = true
		router.Route(esc())
		r.busy = false
		router.Route(esc())
		if r.has("rewind") {
			t.Fatal("rewound after an interrupt")
		}
	})

	t.Run("while busy then idle is not a double press either", func(t *testing.T) {
		// Extra case beyond the TS suite: busy Esc never arms lastEsc, so an
		// idle Esc right after it is treated as a fresh first press, not a
		// second one.
		r, router, _ := routed()
		r.busy = true
		router.Route(esc())
		r.busy = false
		if router.Route(esc()) {
			t.Fatal("a single idle Esc after an interrupt was consumed")
		}
		if r.has("rewind") {
			t.Fatal("rewound after an interrupt")
		}
	})
}

func TestCtrlC(t *testing.T) {
	t.Run("clears the input rather than exiting on the first press", func(t *testing.T) {
		// One press exiting would make a mistyped line cost the session.
		r, router, _ := routed()
		router.Route(ctrlC())
		if !r.has("clearInput") {
			t.Fatalf("calls = %v", r.calls)
		}
		if r.has("exit") {
			t.Fatal("exited on the first press")
		}
	})

	t.Run("exits on a second press within the window", func(t *testing.T) {
		r, router, _ := routed()
		router.Route(ctrlC())
		router.Route(ctrlC())
		if !r.has("exit") {
			t.Fatalf("calls = %v", r.calls)
		}
	})

	t.Run("does not exit when the presses are far apart", func(t *testing.T) {
		r, router, clock := routed()
		router.Route(ctrlC())
		advance(clock, DoublePress+time.Millisecond)
		router.Route(ctrlC())
		if r.has("exit") {
			t.Fatal("exited despite the gap")
		}
	})

	t.Run("interrupts a running turn instead of clearing the input", func(t *testing.T) {
		r, router, _ := routed()
		r.busy = true
		router.Route(ctrlC())
		if !r.has("interrupt") {
			t.Fatalf("calls = %v", r.calls)
		}
		if r.has("clearInput") {
			t.Fatal("cleared the input while busy")
		}
	})

	t.Run("says that a second press exits, rather than leaving it to be guessed", func(t *testing.T) {
		r, router, _ := routed()
		router.Route(ctrlC())
		if !r.has("hint") {
			t.Fatalf("calls = %v", r.calls)
		}
	})
}

func TestCtrlD(t *testing.T) {
	t.Run("exits on an empty line", func(t *testing.T) {
		r, router, _ := routed()
		if !router.Route(ctrlD()) {
			t.Fatal("not consumed")
		}
		if !r.has("exit") {
			t.Fatalf("calls = %v", r.calls)
		}
	})

	t.Run("leaves a line with text to the editor", func(t *testing.T) {
		// On a non-empty line Ctrl+D is delete-forward; exiting would throw
		// away what was typed.
		r, router, _ := routed()
		r.input = true
		if router.Route(ctrlD()) {
			t.Fatal("consumed a Ctrl+D the editor should have handled")
		}
		if r.has("exit") {
			t.Fatal("exited with input on the line")
		}
	})
}

func TestPassesOrdinaryTypingStraightThrough(t *testing.T) {
	r, router, _ := routed()
	msgs := []tea.KeyPressMsg{
		{Code: 'a', Text: "a"},
		{Code: 'Z', Text: "Z"},
		{Code: tea.KeySpace, Text: " "},
		{Code: tea.KeyEnter},
		{Code: tea.KeyTab},
	}
	for _, m := range msgs {
		if router.Route(m) {
			t.Fatalf("%q was consumed", m.String())
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestUnknownPrintableRunePassesThrough(t *testing.T) {
	r, router, _ := routed()
	if router.Route(tea.KeyPressMsg{Code: '~', Text: "~"}) {
		t.Fatal("consumed an unbound printable key")
	}
	if len(r.calls) != 0 {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestRouteReleaseIsANoOp(t *testing.T) {
	r, router, _ := routed()
	// A release of a key the router would otherwise act on, e.g. Ctrl+R,
	// must never trigger the action: pi-tui's rationale (keys.ts:89-91)
	// still applies even though bubbletea's release events are opt-in and
	// this app never opts in.
	router.RouteRelease(tea.KeyReleaseMsg{Code: 'r', Mod: tea.ModCtrl})
	if len(r.calls) != 0 {
		t.Fatalf("calls = %v, want none", r.calls)
	}
}
