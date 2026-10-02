package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// A slash command whose work can take minutes (/compact, waiting on a
// model) returns it as commands.Result.Background. It runs here, on a Cmd
// goroutine, never on Update: run on Update, a /compact against a model
// that never answered froze the whole event loop, so Esc, Ctrl+C and even
// SIGTERM (which bubbletea delivers as a message to that same loop) did
// nothing, and only SIGKILL ended the process.
//
// While it runs the busy line shows BusyLabel (and, for compaction, what
// the model is doing: MsgCompaction), the input still takes typing, a
// submitted line queues until the command is done, and Esc or the first
// Ctrl+C cancels its context.

// backgroundState is the background command in flight.
type backgroundState struct {
	cancel     context.CancelFunc
	cancelNote string
}

// msgBackgroundDone carries a background command's outcome back to Update.
type msgBackgroundDone struct {
	result    commands.Result
	err       error
	cancelled bool
}

// startBackground runs handled.Background off the event loop.
func (m Model) startBackground(handled commands.Result) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.background = &backgroundState{cancel: cancel, cancelNote: handled.CancelNote}
	label := handled.BusyLabel
	if label == "" {
		label = "Working"
	}
	m.spinner.StartLabel(label)
	m.footer.SetBusy(true)
	m = m.syncPromptPlaceholder()
	run := handled.Background
	name := handled.Name
	cmd := func() tea.Msg {
		res, err := run(ctx)
		if res.Name == "" {
			res.Name = name
		}
		return msgBackgroundDone{result: res, err: err, cancelled: ctx.Err() != nil}
	}
	return m, tea.Batch(cmd, tickCmd())
}

// finishBackground shows a background command's outcome, then runs what
// was typed while it ran, the way finishTurn does for a turn: commands and
// `!` lines held in m.deferredCmds first (drainDeferredCommands), then the
// queued text as the next message. If it was cancelled, the queued text
// goes back into the input instead, as after an interrupted turn.
func (m Model) finishBackground(msg msgBackgroundDone) (tea.Model, tea.Cmd) {
	bg := m.background
	m.background = nil
	if bg != nil {
		bg.cancel()
	}
	m.spinner.Stop()
	m.footer.SetBusy(false)
	queued := m.queued
	m.queued = nil
	m.spinner.SetQueueLen(0)

	switch {
	case msg.cancelled:
		note := "Cancelled."
		if bg != nil && bg.cancelNote != "" {
			note = bg.cancelNote
		}
		if len(queued) > 0 {
			restored := strings.Join(queued, "\n")
			if typed := m.editor.Value(); typed != "" {
				restored += "\n" + typed
			}
			m.editor.SetValue(restored)
			note += " Your queued message is back in the input."
		}
		m.commitNote(note)
		queued = nil
	case msg.err != nil:
		m.commit(RenderError(msg.err.Error()))
	default:
		res := msg.result
		m = m.commitCommandOutput(&res)
	}
	m = m.syncPromptPlaceholder()
	m = m.refreshMode()

	m, drained := m.drainDeferredCommands()
	if len(queued) == 0 {
		return m, drained
	}
	text := strings.Join(queued, "\n")
	switch {
	case m.background != nil:
		// A deferred command started another background command: the
		// text keeps waiting for that one.
		m.queued = queued
		m.spinner.SetQueueLen(len(queued))
		return m, drained
	case m.busy:
		// A deferred command started a turn: the text joins it as a
		// mid-turn follow-up.
		m.queued = queued
		if m.cfg.Lane != nil {
			if err := m.cfg.Lane.Steer(text); err != nil {
				m.commit(RenderError(err.Error()))
			}
		}
		return m, drained
	case m.dialog != nil:
		m.editor.SetValue(text)
		return m, drained
	}
	next, cmd := m.handleSubmit(text)
	return next, tea.Batch(drained, cmd)
}

// MsgCompaction is a compaction's progress for the busy line, from the
// harness's compaction_start/progress/end events (Bridge.Wire). Label is
// empty when it ends.
type MsgCompaction struct {
	Label string
	Done  bool
}

// applyCompaction shows what a running compaction is doing on the busy
// line, whether /compact started it (the background command's spinner) or
// it runs inside a turn (auto-compaction, or a request that would not fit
// the window), and restores the turn's own label when it ends.
func (m Model) applyCompaction(msg MsgCompaction) Model {
	if msg.Done {
		if m.background == nil {
			m.spinner.ResetLabel()
		}
		return m
	}
	m.spinner.SetLabel(msg.Label)
	return m
}

// compactionLabel is the busy-line text for a compaction: which part, and
// whether the model is still reading the prompt (nothing streams until it
// has) or already writing the summary. The spinner adds the elapsed time.
func compactionLabel(model string, part, parts, promptTokens, outputTokens int) string {
	label := "Compacting conversation"
	if parts > 1 {
		label += fmt.Sprintf(" (part %d of %d)", part, parts)
	}
	if model == "" {
		return label
	}
	if outputTokens == 0 {
		return fmt.Sprintf("%s · %s is reading ~%s tokens", label, model, FormatTokens(promptTokens))
	}
	return fmt.Sprintf("%s · %s is writing the summary (%s tokens)", label, model, FormatTokens(outputTokens))
}
