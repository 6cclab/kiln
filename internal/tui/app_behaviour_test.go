package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/harness"

	"github.com/andrepato/harness/internal/commands"
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

// TestHandleSubmit_QueuedWhileBusy checks defect
// 20260926T232249Z-queued-block-order's fix: a line submitted while m.busy
// (and no prompt is waiting on an answer) does NOT commit a `you` block to
// the transcript right away — it used to, and read out of order, above the
// reply to the turn it interrupted. Instead it lands in m.queued (rendered
// by liveTail as a dim "queued" row, RenderQueuedFollowUp) until the lane
// actually drains it (MsgQueue{Len:0} in Update commits it for real, see
// TestUpdate_MsgQueueDrain_CommitsQueuedFollowUps). cfg.Lane is left nil
// here — the harness-level delivery mechanism (Lane.Steer) is verified
// separately and directly in internal/harness/steer_verify_test.go, since
// app.go cannot itself see whether the harness actually delivers it on the
// next turn.
func TestHandleSubmit_QueuedWhileBusy(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	m.busy = true
	m.editor.SetValue("check the other file too")

	next, _ := m.handleSubmit("check the other file too")
	nm := next.(Model)

	if strings.TrimSpace(nm.editor.Value()) != "" {
		t.Errorf("editor.Value() = %q, want empty after a queued submit", nm.editor.Value())
	}
	if len(nm.queued) != 1 || nm.queued[0] != "check the other file too" {
		t.Errorf("m.queued = %#v, want one pending item with the submitted text", nm.queued)
	}
	if joined := strings.Join(nm.liveTail(80), "\n"); !strings.Contains(joined, "check the other file too") || !strings.Contains(joined, "queued") {
		t.Errorf("liveTail() = %q, want the pending follow-up with its \"queued\" meta", joined)
	}

	// Nothing commits to the transcript yet — the reply to the turn this
	// follow-up interrupted has not landed, and committing now is exactly
	// the out-of-order bug this test guards against.
	time.Sleep(150 * time.Millisecond)
	printed, _ := f.snapshot()
	for _, p := range printed {
		if strings.Contains(p, "check the other file too") {
			t.Errorf("a queued follow-up must not commit to the transcript before the lane drains it, got %q", p)
		}
	}
}

// TestUpdate_MsgQueueDrain_CommitsQueuedFollowUps checks the other half of
// defect 20260926T232249Z-queued-block-order's fix: MsgQueue{Len:0} — sent
// only by the lane's own drain (turn.go's drainInbox, via
// EventQueueUpdate) — commits every pending m.queued item, in order, as a
// plain `you` block with no "queued" meta, and clears m.queued.
func TestUpdate_MsgQueueDrain_CommitsQueuedFollowUps(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	m.busy = true
	m.queued = []string{"also check the login route", "and update the tests too"}

	next, _ := m.Update(MsgQueue{Len: 0})
	nm := next.(Model)

	if len(nm.queued) != 0 {
		t.Errorf("m.queued = %#v, want empty after a drain", nm.queued)
	}
	first := waitForPrinted(t, f, "also check the login route")
	if strings.Contains(first, "queued") {
		t.Errorf("drained follow-up block = %q, must not carry the \"queued\" meta", first)
	}
	second := waitForPrinted(t, f, "and update the tests too")
	if strings.Contains(second, "queued") {
		t.Errorf("drained follow-up block = %q, must not carry the \"queued\" meta", second)
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

// TestDeclinedPrompt_CommitsFollowupText pins defect 3: declining a
// tool-permission prompt through its explicit "No" option must, in
// addition to the existing "✕ Declined …" note, commit an assistant text
// block reading exactly declinedFollowupText (Terminal.dc.html line 303's
// pick(2): the note plus "Okay, I won't run it. What should I do
// instead?").
func TestDeclinedPrompt_CommitsFollowupText(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	m.prompt.pending = &pendingPermission{
		request: PermissionRequest{ToolName: "bash", PrimaryArg: "npm test -- upload"},
		reply:   make(chan PromptChoice, 1),
	}

	// "4" is the Bash prompt's "No" option (promptOptionsFor("bash"):
	// Yes / don't-ask-again / switch-to-auto / No) — an outright decline,
	// not the feedback-then-Enter path, so this also checks the plain
	// "No" option commits the follow-up, not just Esc.
	next, _ := m.handleKey(charKey('4'))
	_ = next.(Model)

	declineNote := waitForPrinted(t, f, "Declined npm test -- upload")
	if !strings.Contains(declineNote, "✕") {
		t.Errorf("decline note = %q, want the ✕ marker", declineNote)
	}
	waitForPrinted(t, f, declinedFollowupText)
}

// TestEscInterruptsBusyPrompt_SuppressesFollowupText pins defect 5 (Esc
// must decline AND interrupt the turn while a tool-permission prompt is
// queued behind concurrent work) together with defect 3's caveat: an
// Esc-driven interrupt supersedes the "what should I do instead" text —
// finishTurn's own "■ Interrupted…" note is about to answer that same
// question once the abort actually lands, so showing both would read as
// two different answers to the same moment. cfg.Lane is a zero-value
// *harness.Lane (no running operation), so Abort() finds nothing to
// cancel and returns an error that handleKey discards — enough to prove
// the interrupt path runs without panicking; whether a real Lane's
// context actually gets cancelled is exercised at the harness level, not
// here (see internal/harness's own Abort tests).
func TestEscInterruptsBusyPrompt_SuppressesFollowupText(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	m.busy = true
	m.cfg.Lane = &harness.Lane{}
	m.prompt.pending = &pendingPermission{
		request: PermissionRequest{ToolName: "bash", PrimaryArg: "npm test -- upload"},
		reply:   make(chan PromptChoice, 1),
	}

	next, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	nm := next.(Model)

	waitForPrinted(t, f, "Declined npm test -- upload")
	// Force the commit queue to drain past this point before asserting
	// an absence — Bridge.Commit lands on its own goroutine.
	nm.commitNote("sentinel-after-esc-interrupt")
	waitForPrinted(t, f, "sentinel-after-esc-interrupt")

	printed, _ := f.snapshot()
	for _, p := range printed {
		if strings.Contains(p, declinedFollowupText) {
			t.Errorf("committed %q; an Esc-driven interrupt must not also show the decline follow-up text", p)
		}
	}
}

// TestLiveTail_HidesToolGroupRowWhileBusy pins defect 4: the tool-group's
// own live "● Running N shell commands…" row must not render alongside
// the busy line, which is up for exactly as long as m.group can be
// non-nil (flushGroup always empties it before a turn's busy flag clears
// or a prompt opens — see liveTail's doc comment). The row is still
// produced once idle, so accumulation into the group itself is
// untouched — only the redundant live rendering is suppressed.
func TestLiveTail_HidesToolGroupRowWhileBusy(t *testing.T) {
	m := newTestModel()
	m.group = &toolGroup{kind: GroupBash, views: []ToolCallView{{Name: "Bash"}, {Name: "Bash"}}}

	m.busy = true
	for _, l := range m.liveTail(100) {
		if strings.Contains(l, "shell command") {
			t.Errorf("busy liveTail contains the group row %q; defect 4 requires it be suppressed while the busy line is also up", l)
		}
	}

	m.busy = false
	found := false
	for _, l := range m.liveTail(100) {
		if strings.Contains(l, "shell command") {
			found = true
		}
	}
	if !found {
		t.Errorf("idle liveTail dropped the group row entirely; want it still rendered when not busy")
	}
}

// TestModelSwitchNote_WaitsForDialogEcho: a switch made from /model's open
// dialog used to commit its "Model: …" note straight away, above the
// "/model" echo the app commits only when the dialog closes
// (qa/findings *model-dialog-stale-state). The note now lands after it.
func TestModelSwitchNote_WaitsForDialogEcho(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	m.dialogEcho = "/model"
	m.dialog = NewCommandDialog(commands.ModalSpec{Title: "Select model"})
	next, _ := m.Update(msgModelSwitchNote{Text: "Model: faux/faux-2 · small tier · 32.8k usable"})
	m = next.(Model)
	if printed, _ := f.snapshot(); len(printed) != 0 {
		t.Fatalf("note committed while the dialog was open: %q", printed)
	}
	m = m.closeDialog()
	waitForPrinted(t, f, "Model: faux/faux-2")
	printed, _ := f.snapshot()
	echo, note := -1, -1
	for i, p := range printed {
		if strings.Contains(p, "/model") && echo < 0 {
			echo = i
		}
		if strings.Contains(p, "Model: faux/faux-2") {
			note = i
		}
	}
	if echo < 0 || note < echo {
		t.Errorf("want the /model echo before the switch note, got echo=%d note=%d in %q", echo, note, printed)
	}
}
