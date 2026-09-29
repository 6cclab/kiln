//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// TestTUI_MemoryHandsTheTerminalToTheEditor: /memory suspends kiln and runs
// $EDITOR in the foreground, so a terminal editor gets the keyboard. The
// stand-in editor reads one line from the terminal and appends it to the
// file; had kiln kept reading the terminal, the line would have gone to
// kiln's input instead. The note after it exits says whether it saved.
func TestTUI_MemoryHandsTheTerminalToTheEditor(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	editor := filepath.Join(t.TempDir(), "ed.sh")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf '\\033[?1049h\\033[HEDITOR-OPEN'\nread line\nprintf '%s\\n' \"$line\" >> \"$1\"\nprintf '\\033[?1049l'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tuiExtraOpts = []screen.Option{screen.WithEnv("EDITOR", editor), screen.WithEnv("VISUAL", "")}
	t.Cleanup(func() { tuiExtraOpts = nil })

	addr, _ := startFaux(t, "model: faux-1\nsteps: []\n")
	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	defer s.Close()
	waitReady(t, s)
	s.Send("/memory project")
	s.SendKey("enter")
	if err := s.WaitFor("EDITOR-OPEN", 5*time.Second); err != nil {
		t.Fatalf("editor never took the terminal: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	s.Send("remember the gate")
	s.SendKey("enter")
	if err := s.WaitFor("Saved CLAUDE.md.", 5*time.Second); err != nil {
		t.Fatalf("no saved note after the editor exited: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	waitTurnSettled(t, s)
	if rows := strings.Join(s.Rows(), "\n"); strings.Count(rows, "describe a task") != 1 {
		t.Errorf("want one input box after the editor exits, got %d:\n%s", strings.Count(rows, "describe a task"), rows)
	}
	got, err := os.ReadFile(filepath.Join(proj, "CLAUDE.md"))
	if err != nil || !strings.Contains(string(got), "remember the gate") {
		t.Errorf("CLAUDE.md = %q (%v), want the line typed into the editor", got, err)
	}
}
