package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// TestShellExitStartsATurnWhenIdle: like Claude Code, a background shell
// that ends while kiln is idle becomes a turn, so the model carries on
// with what waited on it; the transcript gets a note, not a "you" block.
func TestShellExitStartsATurnWhenIdle(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	code := 0
	m = updateModel(t, m, MsgShellExited{ID: "bash_1", Command: "go test ./...", ExitCode: &code})
	if !m.busy {
		t.Fatal("an idle session did not start a turn for the shell's exit")
	}
	// The note may wrap, with colour codes between its words; compare the
	// plain text.
	want := `Background command "go test ./..." completed (exit code 0) · bash_1`
	deadline := time.Now().Add(10 * time.Second)
	for {
		printed, _ := f.snapshot()
		if strings.Contains(strings.Join(strings.Fields(ansi.Strip(strings.Join(printed, " "))), " "), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no note %q in %q", want, printed)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestShellExitWhileBusyDoesNotStartAnotherTurn: during a turn the note
// is queued for the running turn instead.
func TestShellExitWhileBusyDoesNotStartAnotherTurn(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.busy = true
	turns := m.turn
	m = updateModel(t, m, MsgShellExited{ID: "bash_2", Command: "make build", Failed: true})
	if m.turn != turns {
		t.Errorf("a second turn started while one was running")
	}
}

func TestFrameShellExit(t *testing.T) {
	code := 2
	got := frameShellExit(MsgShellExited{ID: "bash_3", Command: "npm  run\n build", ExitCode: &code})
	for _, want := range []string{"<task-notification>", "<task-id>bash_3</task-id>", "<status>completed</status>",
		`<summary>Background command "npm run build" completed (exit code 2)</summary>`, `bash_output({id: "bash_3"})`} {
		if !strings.Contains(got, want) {
			t.Errorf("notification lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(frameShellExit(MsgShellExited{ID: "bash_4", Command: "x", Failed: true}), "<status>failed</status>") {
		t.Error("a failed shell is not reported as failed")
	}
}
