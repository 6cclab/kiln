//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTUI_AltArrowMovesByWord sends the bytes macOS terminals emit for
// Option+Left/Right in their default profiles (CSI 1;3 D / C) and checks the
// cursor moves by a word, not a character.
func TestTUI_AltArrowMovesByWord(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, "model: faux-1\nsteps:\n  - text: \"ok\"\n")
	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("hello world")
	s.Send("\x1b[1;3D")
	time.Sleep(150 * time.Millisecond)
	s.Send("NEW")
	if err := s.WaitFor("hello NEWworld", 2*time.Second); err != nil {
		t.Fatalf("alt+left did not move back a word:\n%s", strings.Join(s.Rows(), "\n"))
	}
	s.Send("\x1b[1;3C")
	time.Sleep(150 * time.Millisecond)
	s.Send("END")
	if err := s.WaitFor("hello NEWworldEND", 2*time.Second); err != nil {
		t.Fatalf("alt+right did not move forward a word:\n%s", strings.Join(s.Rows(), "\n"))
	}
}
