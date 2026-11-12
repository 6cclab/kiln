package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/crash"
)

// The SessionStart hooks of a folder that was not trusted at launch wait
// for the trust dialog. Once it is accepted they run on a goroutine of
// their own, and a prompt waits for them, as in Claude Code (its submit
// path awaits awaitSessionStartHooks before the first query): the wait is
// a background operation, so the busy line says so, a line typed meanwhile
// queues, and Esc stops the wait and puts the queued line back. Esc does
// not stop the hooks, which Claude Code does not either (it races the
// hooks' promise against the turn's abort signal); their output is kept
// for the next prompt, and a prompt sent while they still run waits again.

// sessionStartRun is the hooks' run. lines is written before done closes,
// and read (by take, on the Update goroutine) only after.
type sessionStartRun struct {
	done  chan struct{}
	lines []string
	taken bool
}

func (r *sessionStartRun) finished() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// msgSessionStartDone says the hooks finished, whether or not a prompt
// is waiting for them.
type msgSessionStartDone struct{}

// startTrustedSessionStart runs the hooks and waits for them at once, so
// a prompt typed while they run queues for their output.
func (m Model) startTrustedSessionStart(run func() []string) (tea.Model, tea.Cmd) {
	r := &sessionStartRun{done: make(chan struct{})}
	crash.Go(func() {
		defer close(r.done)
		r.lines = run()
	})
	m.sessionStart = r
	m, wait := m.waitSessionStart()
	finished := func() tea.Msg {
		<-r.done
		return msgSessionStartDone{}
	}
	return m, tea.Batch(wait, finished)
}

// waitSessionStart starts the background operation that waits for the
// running hooks. It ends when they finish (finishBackground then adds
// their output before it sends what was queued) or on Esc.
func (m Model) waitSessionStart() (Model, tea.Cmd) {
	r := m.sessionStart
	ctx, cancel := context.WithCancel(context.Background())
	m.background = &backgroundState{cancel: cancel, cancelNote: "Stopped waiting for session start hooks."}
	m.spinner.StartLabel("Running session start hooks")
	m.footer.SetBusy(true)
	m = m.syncPromptPlaceholder()
	wait := func() tea.Msg {
		select {
		case <-r.done:
			return msgBackgroundDone{}
		case <-ctx.Done():
			return msgBackgroundDone{cancelled: true}
		}
	}
	return m, tea.Batch(wait, tickCmd())
}

// collectSessionStart moves the hooks' output into the context of the
// next prompt once they have finished.
func (m Model) collectSessionStart() Model {
	r := m.sessionStart
	if r == nil || !r.finished() {
		return m
	}
	if !r.taken {
		r.taken = true
		m.startupContext = append(m.startupContext, r.lines...)
	}
	m.sessionStart = nil
	return m
}

// sessionStartPending reports hooks still running with nothing waiting
// for them: a prompt submitted now must wait.
func (m Model) sessionStartPending() bool {
	return m.sessionStart != nil && !m.sessionStart.finished() && m.background == nil && !m.busy
}
