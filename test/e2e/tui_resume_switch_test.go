//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTUI_ResumeSwitchesSessionInPlace: /resume <id> in a running kiln
// ends this session and continues the chosen one in the same terminal:
// its transcript is replayed, and the next turn lands in its file.
func TestTUI_ResumeSwitchesSessionInPlace(t *testing.T) {
	addr, _ := startFaux(t, `model: faux-1
steps:
  - text: "first session answer"
    end_turn: true
  - text: "back in the first session"
`)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	if res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "remember the gate", "--output-format", "text"); res.Code != 0 {
		t.Fatalf("seeding run: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	first := sessionFile(t, sessDir, proj)
	id := sessionHeaderID(t, first)
	linesBefore := lineCount(t, first)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	defer s.Close()
	waitReady(t, s)
	s.Send("/resume " + id[:8])
	s.SendKey("enter") // completes the id from the picker
	if err := s.WaitFor("/resume "+id, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("enter")
	if err := s.WaitFor("first session answer", 10*time.Second); err != nil {
		t.Fatalf("the resumed session's transcript never appeared: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	waitReady(t, s)
	if rows := strings.Join(s.Rows(), "\n"); strings.Count(rows, "K I L N") != 1 || strings.Contains(rows, "/resume") {
		t.Errorf("the old session's screen is still showing after the switch:\n%s", rows)
	}

	s.Send("are we back")
	s.SendKey("enter")
	if err := s.WaitFor("back in the first session", 10*time.Second); err != nil {
		t.Fatalf("no reply after switching: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	waitTurnSettled(t, s)
	if lineCount(t, first) <= linesBefore {
		t.Errorf("the turn after /resume did not land in the resumed session's file %s", first)
	}
}
