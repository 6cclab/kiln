//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTUI_Esc_KeepsThePartialReply: Esc while a reply is streaming keeps
// what it had written on screen, above the "Interrupted" note, as Claude
// Code does. It used to vanish, leaving only the note under the prompt.
func TestTUI_Esc_KeepsThePartialReply(t *testing.T) {
	// The partial text is one 8-rune chunk: kiln's live stream render is
	// throttled and shows only the first delta of a burst until the next
	// one arrives, so a longer text would sit half-drawn while held.
	script := "model: faux-1\nsteps:\n  - text: \"Partial.\"\n  - hold: stream\n  - text: \"never sent\"\n    end_turn: true\n"
	proj, home, sessDir, addr, _ := tuiFixture(t, script)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)

	s.Send("write the design doc")
	s.SendKey("enter")
	if err := s.WaitFor("Partial.", 5*time.Second); err != nil {
		t.Fatalf("the reply never started streaming: %v", err)
	}
	s.SendKey("esc")
	if err := s.WaitFor("Interrupted. Tell kiln what to do instead.", 5*time.Second); err != nil {
		t.Fatalf("never saw the interrupted note: %v", err)
	}
	waitTurnSettled(t, s)
	joined := strings.Join(s.Rows(), "\n")
	reply := strings.Index(joined, "Partial.")
	note := strings.Index(joined, "Interrupted. Tell kiln")
	if reply < 0 || note < reply {
		t.Fatalf("the partial reply is not on screen above the note:\n%s", joined)
	}
	assertFooterInvariant(t, s)
}
