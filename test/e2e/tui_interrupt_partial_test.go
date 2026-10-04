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
	script := "model: faux-1\nsteps:\n  - chunk_delay: 60ms\n    text: \"" +
		strings.Repeat("Partial design words stream in slowly. ", 30) + "\"\n    end_turn: true\n"
	proj, home, sessDir, addr, _ := tuiFixture(t, script)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)

	s.Send("write the design doc")
	s.SendKey("enter")
	if err := s.WaitFor("Partial design words", 5*time.Second); err != nil {
		t.Fatalf("the reply never started streaming: %v", err)
	}
	s.SendKey("esc")
	if err := s.WaitFor("Interrupted. Tell kiln what to do instead.", 5*time.Second); err != nil {
		t.Fatalf("never saw the interrupted note: %v", err)
	}
	if err := waitQuiescent(s, 200*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(s.Rows(), "\n")
	reply := strings.Index(joined, "Partial design words")
	note := strings.Index(joined, "Interrupted. Tell kiln")
	if reply < 0 || note < reply {
		t.Fatalf("the partial reply is not on screen above the note:\n%s", joined)
	}
	assertFooterInvariant(t, s)
}
