//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTUI_ClearStartsFresh: after /clear the next turn's request carries
// none of the earlier conversation, and in fullscreen the old turns leave
// the screen (inline mode cannot unprint native scrollback).
func TestTUI_ClearStartsFresh(t *testing.T) {
	script := `
model: faux-1
steps:
  - text: "First reply."
    end_turn: true
  - text: "Second reply."
`
	proj, home, sessDir, addr, requests := tuiFixture(t, script)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--fullscreen", "--permission-mode", "bypassPermissions")
	waitReady(t, s)
	s.Send("remember the word pelican")
	s.SendKey("enter")
	if err := s.WaitFor("First reply.", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, s)
	s.Send("/clear")
	s.SendKey("enter")
	if err := s.WaitFor("Conversation cleared", 3*time.Second); err != nil {
		t.Fatalf("no clear note:\n%s", strings.Join(s.Rows(), "\n"))
	}
	// The note sits under the redrawn banner, not above it.
	screen := strings.Join(s.Rows(), "\n")
	if tips, note := strings.Index(screen, "/ commands"), strings.Index(screen, "Conversation cleared"); tips < 0 || note < tips {
		t.Errorf("clear note (at %d) is not below the banner (tips at %d):\n%s", note, tips, screen)
	}
	s.Send("what word")
	s.SendKey("enter")
	if err := s.WaitFor("Second reply.", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	reqs := requests()
	if last := string(reqs[len(reqs)-1]); strings.Contains(last, "pelican") {
		t.Errorf("request after /clear still carries the earlier conversation: %s", last)
	}
	if strings.Contains(strings.Join(s.Rows(), "\n"), "pelican") {
		t.Errorf("earlier turn still on screen after /clear:\n%s", strings.Join(s.Rows(), "\n"))
	}
}
