package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// newTestModelWithBridge is newTestModel with a real Bridge wired to a
// fakeSink, so a test can inspect what handleSubmit/finishTurn commit
// without a live Bubbletea program.
func newTestModelWithBridge(t *testing.T) (Model, *fakeSink) {
	t.Helper()
	b := NewBridge("/tmp")
	t.Cleanup(b.Stop)
	f := &fakeSink{}
	b.setSink(f)
	m := NewModel(Config{
		Cwd:         "/tmp",
		ModelLabel:  "ollama/qwen3.8",
		InitialMode: "manual",
		StartedAt:   time.Unix(0, 0),
		Bridge:      b,
	})
	m.width, m.height = 100, 30
	return m, f
}

// waitForPrinted polls f.snapshot() until at least one Println'd block
// contains want, or fails after a short deadline (Bridge.Commit lands on
// its own committer goroutine, not the caller's).
func waitForPrinted(t *testing.T, f *fakeSink, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		printed, _ := f.snapshot()
		for _, p := range printed {
			if strings.Contains(p, want) {
				return p
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for a committed block containing %q", want)
	return ""
}

// TestHandleSubmit_QueuedWhileBusy checks E's spec: a line submitted while
// m.busy (and no prompt is waiting on an answer) commits the `you` block
// immediately with a "queued" meta and clears the editor, instead of
// running the normal command/mention/hook pipeline and starting a second
// turn. cfg.Lane is left nil here — the harness-level delivery mechanism
// (Lane.Steer) is verified separately and directly in
// internal/harness/steer_verify_test.go, since app.go cannot itself see
// whether the harness actually delivers it on the next turn.
func TestHandleSubmit_QueuedWhileBusy(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	m.busy = true
	m.editor.SetValue("check the other file too")

	next, _ := m.handleSubmit("check the other file too")
	nm := next.(Model)

	if strings.TrimSpace(nm.editor.Value()) != "" {
		t.Errorf("editor.Value() = %q, want empty after a queued submit", nm.editor.Value())
	}

	got := waitForPrinted(t, f, "check the other file too")
	if !strings.Contains(got, "queued") {
		t.Errorf("committed block = %q, want it to carry the \"queued\" meta", got)
	}
}

// TestRetryKey_ConsumedOnlyWithPendingRetryAndEmptyInput drives handleKey
// with a literal "r" keypress in the three cases that matter: a pending
// retry with an empty editor (consumed, cfg.Lane is nil here so there is
// nothing further to assert but that it does not panic and leaves the
// editor untouched), a pending retry with existing input (must type
// normally), and no pending retry at all (must type normally).
func TestRetryKey_ConsumedOnlyWithPendingRetryAndEmptyInput(t *testing.T) {
	rMsg := tea.KeyPressMsg{Code: 'r', Text: "r"}

	t.Run("pending retry, empty input: consumed", func(t *testing.T) {
		m, _ := newTestModelWithBridge(t)
		m.retry = &RetryView{Message: "x", Attempt: 1, Max: 4, Until: time.Now().Add(3 * time.Second)}
		next, _ := m.handleKey(rMsg)
		nm := next.(Model)
		if got := nm.editor.Value(); got != "" {
			t.Errorf("editor.Value() = %q, want empty — the key must not reach the editor", got)
		}
	})

	t.Run("pending retry, non-empty input: types normally", func(t *testing.T) {
		m, _ := newTestModelWithBridge(t)
		m.retry = &RetryView{Message: "x", Attempt: 1, Max: 4, Until: time.Now().Add(3 * time.Second)}
		m.editor.SetValue("queue this")
		next, _ := m.handleKey(rMsg)
		nm := next.(Model)
		if got := nm.editor.Value(); got != "queue thisr" {
			t.Errorf("editor.Value() = %q, want %q", got, "queue thisr")
		}
	})

	t.Run("no pending retry: types normally", func(t *testing.T) {
		m, _ := newTestModelWithBridge(t)
		next, _ := m.handleKey(rMsg)
		nm := next.(Model)
		if got := nm.editor.Value(); got != "r" {
			t.Errorf("editor.Value() = %q, want %q", got, "r")
		}
	})
}
