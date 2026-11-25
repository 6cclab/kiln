package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// MsgShellExited is a background shell (bash_background) that ended on its
// own: exited, or failed to run. A shell killed with kill_shell or /bashes
// sends none; that was deliberate.
type MsgShellExited struct {
	ID       string
	Command  string
	Failed   bool
	ExitCode *int
}

// summary is the one line for the transcript note and the notification.
func (s MsgShellExited) summary() string {
	cmd := strings.Join(strings.Fields(s.Command), " ")
	if r := []rune(cmd); len(r) > 80 {
		cmd = string(r[:80]) + "…"
	}
	switch {
	case s.Failed:
		return fmt.Sprintf("Background command %q failed to run", cmd)
	case s.ExitCode != nil:
		return fmt.Sprintf("Background command %q completed (exit code %d)", cmd, *s.ExitCode)
	}
	return fmt.Sprintf("Background command %q completed", cmd)
}

// frameShellExit is the notification the model receives, in Claude Code's
// task-notification shape (2.1.319), naming kiln's way to read the output.
func frameShellExit(s MsgShellExited) string {
	status := "completed"
	if s.Failed {
		status = "failed"
	}
	return "<task-notification>\n" +
		"<task-id>" + s.ID + "</task-id>\n" +
		"<status>" + status + "</status>\n" +
		"<summary>" + s.summary() + "</summary>\n" +
		"</task-notification>\n" +
		fmt.Sprintf("Read its output with bash_output({id: %q}) if you need it.", s.ID)
}

// injectShellExit tells the model a background shell ended, as Claude Code
// does: steered into a running turn, or, when kiln is idle, as a turn of
// its own, so work waiting on a build or a test run carries on. The
// transcript shows a note, not a "you" block: nobody typed it.
func (m Model) injectShellExit(s MsgShellExited) (tea.Model, tea.Cmd) {
	framed := frameShellExit(s)
	m.commitNote(s.summary() + " · " + s.ID)
	if m.busy || m.prompt.Active() || m.background != nil || m.dialog != nil {
		// A turn is running (it reads the note at its next step), or
		// someone is answering a prompt or a command runs: the note goes
		// with the next request rather than starting a turn now.
		if m.cfg.Lane != nil {
			if err := m.cfg.Lane.Steer(framed); err != nil {
				m.commit(RenderError(err.Error()))
			}
		}
		return m, nil
	}
	return m.beginTurn(framed, framed, nil)
}
