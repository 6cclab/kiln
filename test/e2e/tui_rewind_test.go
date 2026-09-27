//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTUI_RewindEnterApplies: Esc Esc on an empty prompt opens Rewind;
// choosing an earlier turn and pressing Enter rewinds to before it. In a
// real terminal Enter left the dialog open with nothing applied.
func TestTUI_RewindEnterApplies(t *testing.T) {
	script := `
model: faux-1
steps:
  - text: "First turn reply."
    end_turn: true
  - text: "Second turn reply."
`
	proj, home, sessDir, addr, _ := tuiFixture(t, script)
	s := startTUI(t, 120, 40, proj, home, sessDir, addr, "--permission-mode", "dontAsk")
	waitReady(t, s)
	for _, p := range []struct{ in, out string }{{"turn one", "First turn reply."}, {"turn two", "Second turn reply."}} {
		s.Send(p.in)
		s.SendKey("enter")
		if err := s.WaitFor(p.out, 5*time.Second); err != nil {
			t.Fatalf("%v\n%s", err, strings.Join(s.Rows(), "\n"))
		}
		waitTurnSettled(t, s)
	}
	s.SendKey("esc")
	s.SendKey("esc")
	if err := s.WaitFor("Restore the code", 3*time.Second); err != nil {
		t.Fatalf("rewind dialog did not open:\n%s", strings.Join(s.Rows(), "\n"))
	}
	// Selection is colour-only (no marker glyph), so there is no text to
	// wait on between the two keys; give the dialog time to take "up".
	time.Sleep(500 * time.Millisecond)
	s.SendKey("up")
	time.Sleep(500 * time.Millisecond)
	s.SendKey("enter")
	if err := s.WaitFor("Rewound to before: turn two", 3*time.Second); err != nil {
		t.Fatalf("enter did not apply the rewind:\n%s", strings.Join(s.Rows(), "\n"))
	}
}
