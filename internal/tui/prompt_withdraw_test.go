package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/tools"
)

// A caller that gives up waiting (its context ended) withdraws its
// prompt: a queued one never shows, and one on screen goes away, rather
// than leaving a dialog whose answer nobody reads.
func TestPromptQueue_WithdrawnPromptNeverShows(t *testing.T) {
	m := busyModelRunning(t, "Running bash")
	first := make(chan PromptChoice, 1)
	stale := make(chan PlanReply, 1)
	m = updateModel(t, m, bashPrompt(first))
	m = updateModel(t, m, MsgPlanPrompt{Plan: "1. do it", Reply: stale})
	m = updateModel(t, m, MsgPromptWithdrawn{Reply: stale})
	m = press(t, m, "1")
	gotReply(t, "the first permission prompt", first)
	if m.prompt.Active() {
		t.Fatal("a withdrawn prompt showed after the one before it was answered")
	}
	if got := m.spinner.Label(); got != "Running bash" {
		t.Errorf("label = %q, want %q", got, "Running bash")
	}

	shown := make(chan PromptChoice, 1)
	m = updateModel(t, m, bashPrompt(shown))
	m = updateModel(t, m, MsgPromptWithdrawn{Reply: shown})
	if m.prompt.Active() {
		t.Fatal("a withdrawn prompt stayed on screen")
	}
	// The call it asked about did not go ahead: the row returns to the
	// turn's gerund, as for a declined prompt.
	if got, want := m.spinner.Label(), PickLabel(0); got != want {
		t.Errorf("label after the on-screen prompt was withdrawn = %q, want %q", got, want)
	}
}

// recordSink captures what the bridge sends the program.
type recordSink struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (r *recordSink) Send(msg tea.Msg) {
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
}
func (r *recordSink) Println(...any) {}

func (r *recordSink) snapshot() []tea.Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tea.Msg(nil), r.msgs...)
}

// Each approver withdraws its prompt when its context ends before an
// answer arrives.
func TestBridgeApprovers_WithdrawOnCancel(t *testing.T) {
	cases := map[string]func(b *Bridge, ctx context.Context){
		"permission": func(b *Bridge, ctx context.Context) {
			_, _ = b.Prompter("/tmp")(ctx, permission.Request{ToolName: "bash"})
		},
		"plan": func(b *Bridge, ctx context.Context) {
			_, _ = b.PlanApprover()(ctx, "1. do it")
		},
		"question": func(b *Bridge, ctx context.Context) {
			_, _ = b.AskUserApprover()(ctx, []tools.AskUserQuestion{{Question: "Which?"}})
		},
	}
	for name, ask := range cases {
		t.Run(name, func(t *testing.T) {
			b := NewBridge("/tmp")
			t.Cleanup(b.Stop)
			sink := &recordSink{}
			b.setSink(sink)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { ask(b, ctx); close(done) }()
			waitFor(t, func() bool { return len(sink.snapshot()) >= 1 })
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("approver did not return after its context was cancelled")
			}
			waitFor(t, func() bool { return len(sink.snapshot()) >= 2 })
			msgs := sink.snapshot()
			w, ok := msgs[1].(MsgPromptWithdrawn)
			if !ok {
				t.Fatalf("second message = %T, want MsgPromptWithdrawn", msgs[1])
			}
			var want any
			switch p := msgs[0].(type) {
			case MsgPermissionPrompt:
				want = p.Reply
			case MsgPlanPrompt:
				want = p.Reply
			case MsgAskUserPrompt:
				want = p.Reply
			default:
				t.Fatalf("first message = %T, want a prompt", msgs[0])
			}
			if w.Reply != want {
				t.Fatal("the withdrawal names a different prompt's reply channel")
			}
		})
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
