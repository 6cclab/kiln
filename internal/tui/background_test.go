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
	deadline := time.After(5 * time.Second)
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
	case <-time.After(2 * time.Second):
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
