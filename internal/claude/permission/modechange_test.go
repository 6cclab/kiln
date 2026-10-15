package permission

import (
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// TestSetModeNotifiesObserversOnChangeOnly: every observer hears a real
// change, with where it came from; setting the mode it already has is not
// a change.
func TestSetModeNotifiesObserversOnChangeOnly(t *testing.T) {
	g := NewGate(GateOptions{Mode: settings.ModeManual})
	var seen []string
	for i := 0; i < 2; i++ {
		g.OnModeChange(func(from, to settings.PermissionMode) {
			seen = append(seen, string(from)+">"+string(to))
		})
	}
	g.SetMode(settings.ModePlan)
	g.SetMode(settings.ModePlan)
	g.SetMode(settings.ModeAuto)
	want := []string{"manual>plan", "manual>plan", "plan>auto", "plan>auto"}
	if len(seen) != len(want) {
		t.Fatalf("observers saw %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("observers saw %v, want %v", seen, want)
		}
	}
}

// TestOnModeChangeObserverMayReadTheGate: observers run outside the lock,
// so one that reads the gate's mode does not deadlock and sees the new
// mode.
func TestOnModeChangeObserverMayReadTheGate(t *testing.T) {
	g := NewGate(GateOptions{Mode: settings.ModeManual})
	var got settings.PermissionMode
	g.OnModeChange(func(_, _ settings.PermissionMode) { got = g.Mode() })
	g.SetMode(settings.ModeAcceptEdits)
	if got != settings.ModeAcceptEdits {
		t.Fatalf("observer read %q, want acceptEdits", got)
	}
}
