package tui

import (
	"context"
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// slowRegistry has one command, /slow, whose background work waits for
// release (or its context) and reports "done".
func slowRegistry(release <-chan struct{}, ran *[]string) *commands.Registry {
	reg := commands.NewRegistry()
	reg.Add(commands.StaticSource(commands.OriginBuiltin, []commands.Command{{
		Name: "slow",
		Run: func(ctx context.Context, args string) (commands.Result, error) {
			return commands.Result{
				BusyLabel:  "Compacting conversation",
				CancelNote: "Compaction cancelled.",
				Background: func(ctx context.Context) (commands.Result, error) {
					select {
					case <-release:
						return commands.Result{Output: []string{"done"}}, nil
					case <-ctx.Done():
						return commands.Result{}, ctx.Err()
					}
				},
			}, nil
		},
	}, {
		// On the immediate allowlist: runs mid-turn and mid-compaction.
		Name: "status",
		Run: func(ctx context.Context, args string) (commands.Result, error) {
			*ran = append(*ran, "status")
			return commands.Result{Output: []string{"status ok"}}, nil
		},
	}, {
		// Not on it: waits until the busy work ends.
		Name: "model",
		Run: func(ctx context.Context, args string) (commands.Result, error) {
			*ran = append(*ran, "model")
			return commands.Result{Output: []string{"model ok"}}, nil
		},
	}}))
	return reg
}

// backgroundDone runs cmd (a tea.Batch) and returns the msgBackgroundDone
// it eventually produces.
func backgroundDone(t *testing.T, cmd tea.Cmd) msgBackgroundDone {
	t.Helper()
	out := make(chan tea.Msg, 4)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, sub := range batch {
					run(sub)
				}
				return
			}
			out <- msg
		}()
	}
	run(cmd)
	deadline := time.After(30 * time.Second)
	for {
		select {
		case msg := <-out:
			if done, ok := msg.(msgBackgroundDone); ok {
				return done
			}
		case <-deadline:
			t.Fatal("background command never finished")
		}
	}
}

func newBackgroundModel(release <-chan struct{}) Model {
	return newBackgroundModelRan(release, new([]string))
}

func newBackgroundModelRan(release <-chan struct{}, ran *[]string) Model {
	m := NewModel(Config{Cwd: "/tmp", ModelLabel: "faux/faux-1", InitialMode: "manual", StartedAt: time.Unix(0, 0), Registry: slowRegistry(release, ran)})
	m.width, m.height = 100, 30
	return m
}

// TestBackgroundCommand_UpdateReturnsAndEscCancels: submitting /slow
// returns from Update at once with the busy line up; a line typed
// meanwhile queues; Esc cancels the command's context and the queued line
// goes back into the input.
func TestBackgroundCommand_UpdateReturnsAndEscCancels(t *testing.T) {
	release := make(chan struct{})
	m := newBackgroundModel(release)
	returned := make(chan struct{})
	var next tea.Model
	var cmd tea.Cmd
	go func() {
		next, cmd = m.handleSubmit("/slow")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(30 * time.Second):
		t.Fatal("Update blocked on the command's background work")
	}
	m = next.(Model)
	if m.background == nil || !m.spinner.Busy() || m.spinner.Label() != "Compacting conversation" {
		t.Fatalf("no busy line while the command runs: background=%v busy=%v label=%q", m.background != nil, m.spinner.Busy(), m.spinner.Label())
	}

	next, _ = m.handleSubmit("follow-up")
	m = next.(Model)
	if len(m.queued) != 1 {
		t.Fatalf("queued = %v, want the follow-up", m.queued)
	}

	next, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	done := backgroundDone(t, cmd)
	if !done.cancelled {
		t.Fatal("Esc did not cancel the background command")
	}
	next, _ = m.Update(done)
	m = next.(Model)
	if m.background != nil || m.spinner.Busy() {
		t.Error("still busy after the cancellation landed")
	}
	if got := m.editor.Value(); got != "follow-up" {
		t.Errorf("input = %q, want the queued follow-up back", got)
	}
}

// TestBackgroundCommand_QueuedLineSentAfter: a line queued while the
// command runs is sent once it finishes.
func TestBackgroundCommand_QueuedLineSentAfter(t *testing.T) {
	release := make(chan struct{})
	m := newBackgroundModel(release)
	next, cmd := m.handleSubmit("/slow")
	m = next.(Model)
	next, _ = m.handleSubmit("follow-up")
	m = next.(Model)
	close(release)
	done := backgroundDone(t, cmd)
	if done.cancelled || done.err != nil {
		t.Fatalf("done = %+v", done)
	}
	next, _ = m.Update(done)
	m = next.(Model)
	if !m.busy {
		t.Error("the queued follow-up did not start a turn")
	}
	if len(m.queued) != 0 {
		t.Errorf("queued = %v after it was sent", m.queued)
	}
}

// TestBackgroundCommand_CommandsLikeMidTurn: during a background command,
// slash commands follow the same rules as during a turn: an immediate one
// (/status) runs at once, any other (/model) waits and runs when the
// background command ends, before the queued text is sent.
func TestBackgroundCommand_CommandsLikeMidTurn(t *testing.T) {
	release := make(chan struct{})
	var ran []string
	m := newBackgroundModelRan(release, &ran)
	next, cmd := m.handleSubmit("/slow")
	m = next.(Model)

	next, _ = m.handleSubmit("/status")
	m = next.(Model)
	if fmt.Sprint(ran) != "[status]" {
		t.Fatalf("ran = %v, want /status to run at once", ran)
	}
	next, _ = m.handleSubmit("/model")
	m = next.(Model)
	if fmt.Sprint(ran) != "[status]" || len(m.deferredCmds) != 1 {
		t.Fatalf("ran = %v, deferred = %v: /model should wait", ran, m.deferredCmds)
	}
	next, _ = m.handleSubmit("follow-up")
	m = next.(Model)

	close(release)
	next, _ = m.Update(backgroundDone(t, cmd))
	m = next.(Model)
	if fmt.Sprint(ran) != "[status model]" {
		t.Errorf("ran = %v, want /model run once the background command ended", ran)
	}
	if !m.busy || len(m.deferredCmds) != 0 {
		t.Errorf("busy = %v, deferred = %v: want the follow-up sent as a turn", m.busy, m.deferredCmds)
	}
}

// trustedModel is a model whose folder was just trusted: hooks is its
// SessionStart, run once the dialog is accepted.
func trustedModel(t *testing.T, hooks func() []string) (Model, tea.Cmd) {
	t.Helper()
	m := NewModel(Config{
		Cwd: "/tmp", ModelLabel: "faux/faux-1", InitialMode: "manual", StartedAt: time.Unix(0, 0),
		TrustedSessionStart: hooks,
	})
	m.width, m.height = 100, 30
	next, cmd := m.Update(MsgTrustAnswered{Trusted: true})
	return next.(Model), cmd
}

func blockedHooks(release <-chan struct{}) func() []string {
	return func() []string {
		<-release
		return []string{"CONTEXT-FROM-SESSION-START"}
	}
}

// TestTrustedSessionStart_PromptWaitsForContext: the SessionStart hooks
// that waited for the trust dialog run once the folder is trusted, and a
// prompt waits for them, as in Claude Code: a line submitted before they
// finish queues and goes out once they do. Before, the hooks ran on a
// goroutine nothing waited for, and a prompt sent first went without
// their context.
func TestTrustedSessionStart_PromptWaitsForContext(t *testing.T) {
	release := make(chan struct{})
	m, cmd := trustedModel(t, blockedHooks(release))
	if m.background == nil {
		t.Fatal("nothing waits for the SessionStart hooks")
	}
	next, _ := m.handleSubmit("go")
	m = next.(Model)
	if m.busy || len(m.queued) != 1 {
		t.Fatalf("the prompt did not wait for SessionStart: busy=%v queued=%v", m.busy, m.queued)
	}
	close(release)
	next, _ = m.Update(backgroundDone(t, cmd))
	m = next.(Model)
	if !m.busy || len(m.queued) != 0 {
		t.Fatalf("the queued prompt was not sent: busy=%v queued=%v", m.busy, m.queued)
	}
	if len(m.startupContext) != 0 || m.sessionStart != nil {
		t.Errorf("the prompt went out without the SessionStart context: left %v, run %v", m.startupContext, m.sessionStart)
	}
}

// TestTrustedSessionStart_ContextKeptForNextPrompt: with nothing queued,
// the SessionStart context waits for the next prompt.
func TestTrustedSessionStart_ContextKeptForNextPrompt(t *testing.T) {
	m, cmd := trustedModel(t, func() []string { return []string{"CONTEXT-FROM-SESSION-START"} })
	next, _ := m.Update(backgroundDone(t, cmd))
	m = next.(Model)
	if m.background != nil || m.busy {
		t.Fatalf("still busy after SessionStart: background=%v busy=%v", m.background != nil, m.busy)
	}
	if len(m.startupContext) != 1 || m.startupContext[0] != "CONTEXT-FROM-SESSION-START" {
		t.Errorf("startupContext = %v", m.startupContext)
	}
}

// TestTrustedSessionStart_EscStopsTheWaitNotTheHooks: Esc stops waiting
// and puts the queued prompt back in the input, but the hooks keep
// running, as in Claude Code (it races their promise against the turn's
// abort signal); their output is kept for the next prompt when they end.
func TestTrustedSessionStart_EscStopsTheWaitNotTheHooks(t *testing.T) {
	release := make(chan struct{})
	m, cmd := trustedModel(t, blockedHooks(release))
	next, _ := m.handleSubmit("go")
	m = next.(Model)
	next, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	done := backgroundDone(t, cmd)
	if !done.cancelled {
		t.Fatal("Esc did not stop the wait")
	}
	next, _ = m.Update(done)
	m = next.(Model)
	if m.background != nil || m.busy {
		t.Errorf("still busy after Esc: background=%v busy=%v", m.background != nil, m.busy)
	}
	if got := m.editor.Value(); got != "go" {
		t.Errorf("input = %q, want the queued prompt back", got)
	}
	run := m.sessionStart
	if run == nil || run.finished() {
		t.Fatal("Esc stopped the hooks")
	}
	close(release)
	<-run.done
	next, _ = m.Update(msgSessionStartDone{})
	m = next.(Model)
	if len(m.startupContext) != 1 || m.sessionStart != nil {
		t.Errorf("the hooks' output was not kept: startupContext=%v run=%v", m.startupContext, m.sessionStart)
	}
}

// TestTrustedSessionStart_PromptAfterEscWaitsAgain: a prompt sent while
// the hooks still run after Esc waits for them again.
func TestTrustedSessionStart_PromptAfterEscWaitsAgain(t *testing.T) {
	release := make(chan struct{})
	m, cmd := trustedModel(t, blockedHooks(release))
	next, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	next, _ = m.Update(backgroundDone(t, cmd))
	m = next.(Model)
	if m.background != nil {
		t.Fatal("Esc did not stop the wait")
	}
	next, cmd = m.handleSubmit("go")
	m = next.(Model)
	if m.background == nil || m.busy || len(m.queued) != 1 {
		t.Fatalf("the prompt did not wait again: background=%v busy=%v queued=%v", m.background != nil, m.busy, m.queued)
	}
	close(release)
	next, _ = m.Update(backgroundDone(t, cmd))
	m = next.(Model)
	if !m.busy || len(m.queued) != 0 {
		t.Fatalf("the queued prompt was not sent: busy=%v queued=%v", m.busy, m.queued)
	}
	if len(m.startupContext) != 0 || m.sessionStart != nil {
		t.Errorf("the prompt went out without the SessionStart context: left %v, run %v", m.startupContext, m.sessionStart)
	}
}
