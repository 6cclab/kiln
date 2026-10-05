package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// floodModel commits n one-line blocks from inside Update in one go (more
// than the bridge's queue once held), then asks the bridge to deliver
// floodDone after them, and quits when it arrives. Every line the program
// printed is recorded off the tea.PrintedLines echo Update receives, so
// the test can check none was lost or reordered.
type floodModel struct {
	b     *Bridge
	n     int
	lines *[]string
}

type floodStart struct{}
type floodDone struct{}

func (m floodModel) Init() tea.Cmd { return func() tea.Msg { return floodStart{} } }

func (m floodModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if body, ok := tea.PrintedLines(msg); ok {
		*m.lines = append(*m.lines, strings.Split(body, "\n")...)
		return m, nil
	}
	switch msg.(type) {
	case floodStart:
		for i := 0; i < m.n; i++ {
			m.b.Commit([]string{fmt.Sprintf("line %d", i)})
		}
		m.b.SendAsync(floodDone{})
	case floodDone:
		return m, tea.Quit
	}
	return m, nil
}

func (m floodModel) View() tea.View { return tea.NewView("") }

// TestBridge_CommitFromUpdateNeverDeadlocks drives a real Bubble Tea
// program (the vendored third_party/bubbletea) whose Update commits more
// blocks than the bridge's queue once held (1024). The committer hands
// each block to the event loop with Program.Println, an unbuffered send
// only the event loop drains between Update calls; when Commit blocked on
// a full queue from inside Update, the committer waited on Update and
// Update waited on the committer, and the program hung for good. The run
// must finish with every line printed exactly once, in order, and the
// SendAsync message after them.
func TestBridge_CommitFromUpdateNeverDeadlocks(t *testing.T) {
	prevMargin := renderMargin
	renderMargin = 0
	t.Cleanup(func() { renderMargin = prevMargin })

	const n = 3000
	b := NewBridge(t.TempDir())
	var lines []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := tea.NewProgram(floodModel{b: b, n: n, lines: &lines},
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignals(),
		tea.WithoutSignalHandler(),
	)
	b.SetProgram(p)

	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, runErr = p.Run()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// Unwedge what can be unwedged so the test binary exits: Stop
		// releases Update from Commit, cancel ends the event loop. The
		// committer stays stuck in Println, which has no way out.
		b.Stop()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		t.Fatalf("program deadlocked: Update committing %d blocks never returned", n)
	}
	b.Stop()
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if len(lines) != n {
		t.Fatalf("printed %d lines, want %d", len(lines), n)
	}
	for i, line := range lines {
		if want := fmt.Sprintf("line %d", i); line != want {
			t.Fatalf("line %d = %q, want %q", i, line, want)
		}
	}
}

// stuckSink is a sink whose every call blocks until release closes: an
// event loop that is not draining, which is the committer's view of an
// Update that is busy, or waiting on the very caller that is committing.
type stuckSink struct {
	release chan struct{}
	mu      sync.Mutex
	printed []string
	sent    []tea.Msg
}

func (s *stuckSink) Println(args ...any) {
	<-s.release
	s.mu.Lock()
	defer s.mu.Unlock()
	s.printed = append(s.printed, fmt.Sprint(args...))
}

func (s *stuckSink) Send(m tea.Msg) {
	<-s.release
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
}

// TestBridge_EnqueueNeverBlocksOnStuckSink checks the property the fix
// rests on, whichever goroutine commits: with the committer stuck handing
// a block to an event loop that is not draining, Commit, SendAsync and
// SetFullscreen still return at once, however many items are queued. A
// producer that waits for space can deadlock even when it is not Update,
// because Update can be waiting on a lock that producer holds (the harness
// event bus runs handlers under invokeMu, and Lane.Steer emits on it from
// Update). Once the sink drains, every item arrives in order, with the
// mode flip still splitting the two sinks.
func TestBridge_EnqueueNeverBlocksOnStuckSink(t *testing.T) {
	prevMargin := renderMargin
	renderMargin = 0
	t.Cleanup(func() { renderMargin = prevMargin })

	b := NewBridge(t.TempDir())
	defer b.Stop()
	s := &stuckSink{release: make(chan struct{})}
	b.setSink(s)

	const n = 5000
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		for i := 0; i < n; i++ {
			b.Commit([]string{fmt.Sprintf("a%d", i)})
		}
		b.SendAsync(floodDone{})
		b.SetFullscreen(true)
		b.Commit([]string{"after"})
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		b.Stop()
		close(s.release)
		t.Fatal("enqueueing blocked while the sink was stuck")
	}
	close(s.release)

	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		got := len(s.sent)
		s.mu.Unlock()
		if got >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sink never received the queued messages (got %d)", got)
		}
		time.Sleep(time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var lines []string
	for _, p := range s.printed {
		lines = append(lines, strings.Split(p, "\n")...)
	}
	if len(lines) != n {
		t.Fatalf("printed %d lines, want %d", len(lines), n)
	}
	for i, line := range lines {
		if want := fmt.Sprintf("a%d", i); line != want {
			t.Fatalf("line %d = %q, want %q", i, line, want)
		}
	}
	if _, ok := s.sent[0].(floodDone); !ok {
		t.Fatalf("first Send = %#v, want floodDone", s.sent[0])
	}
	if app, ok := s.sent[1].(MsgTranscriptAppend); !ok || app.Text != "after" {
		t.Fatalf("second Send = %#v, want MsgTranscriptAppend{after}", s.sent[1])
	}
}
