package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/harness"
)

// busyTestRegistry builds a minimal *commands.Registry for the busy-submit
// routing tests: "status" (on immediateCommandNames) and "compact" (not —
// it mutates the conversation) each just report what they were called
// with, so a test can tell whether handleSubmit ran a command right away
// or deferred it.
func busyTestRegistry(calls *[]string) *commands.Registry {
	reg := commands.NewRegistry()
	reg.Add(commands.StaticSource(commands.OriginBuiltin, []commands.Command{
		{
			Name: "status",
			Run: func(ctx context.Context, args string) (commands.Result, error) {
				*calls = append(*calls, "status")
				return commands.Result{Output: []string{"status ran"}}, nil
			},
		},
		{
			Name: "compact",
			Run: func(ctx context.Context, args string) (commands.Result, error) {
				*calls = append(*calls, "compact")
				return commands.Result{Output: []string{"compact ran"}}, nil
			},
		},
	}))
	return reg
}

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

// TestDeclinedPrompt_CommitsNoteButNoScriptedReply: declining a
// tool-permission prompt through its "No" option commits the "✕ Declined …"
// note and nothing that reads as the model's reply: the model answers the
// refusal itself.
func TestDeclinedPrompt_CommitsNoteButNoScriptedReply(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	m.prompt.pending = &pendingPermission{
		request: PermissionRequest{ToolName: "bash", PrimaryArg: "npm test -- upload", Grantable: true, DontAskRules: []string{"npm test *"}},
		reply:   make(chan PromptChoice, 1),
	}

	next, _ := m.handleKey(charKey('4'))
	nm := next.(Model)

	declineNote := waitForPrinted(t, f, "Declined npm test -- upload")
	if !strings.Contains(declineNote, "✕") {
		t.Errorf("decline note = %q, want the ✕ marker", declineNote)
	}
	nm.commitNote("sentinel-after-decline")
	waitForPrinted(t, f, "sentinel-after-decline")
	printed, _ := f.snapshot()
	for _, p := range printed {
		if strings.Contains(p, "What should I do instead") {
			t.Errorf("committed a scripted reply %q in the model's name", p)
		}
	}
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
		request: PermissionRequest{ToolName: "bash", PrimaryArg: "npm test -- upload", Grantable: true, DontAskRules: []string{"npm test *"}},
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
		if strings.Contains(p, "What should I do instead") {
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

// TestHandleSubmit_ImmediateCommandRunsWhileBusy regression-tests bug #2:
// a read-only/UI-only command (here "status", standing in for the real
// /mcp, /status, …) typed while a turn is running must run right away,
// not be sent to the model as text and not be deferred to the turn's end.
func TestHandleSubmit_ImmediateCommandRunsWhileBusy(t *testing.T) {
	var calls []string
	m, f := newTestModelWithBridge(t)
	m.cfg.Registry = busyTestRegistry(&calls)
	m.busy = true

	next, _ := m.handleSubmit("/status")
	nm := next.(Model)

	if len(calls) != 1 || calls[0] != "status" {
		t.Fatalf("calls = %#v, want [\"status\"] (it must run immediately)", calls)
	}
	if len(nm.queued) != 0 {
		t.Errorf("m.queued = %#v, want empty — an immediate command must not go to Lane.Steer", nm.queued)
	}
	if len(nm.deferredCmds) != 0 {
		t.Errorf("m.deferredCmds = %#v, want empty — an immediate command must not be deferred", nm.deferredCmds)
	}
	if !nm.busy {
		t.Error("busy must stay true — an immediate command does not end the running turn")
	}
	waitForPrinted(t, f, "status ran")
}

// TestHandleSubmit_NonImmediateCommandDefersWhileBusy regression-tests
// the actual bug: Andre typed "/mcp" while a turn was running and it
// reached the model as text ("I'm not Claude Code — I don't have a /mcp
// command"). A non-immediate command ("compact", standing in for /model,
// /clear, /compact, …) must be held, not run, and — critically — must
// never go to Lane.Steer (which is what delivers text to the model).
func TestHandleSubmit_NonImmediateCommandDefersWhileBusy(t *testing.T) {
	var calls []string
	m, _ := newTestModelWithBridge(t)
	m.cfg.Registry = busyTestRegistry(&calls)
	m.busy = true

	next, _ := m.handleSubmit("/compact")
	nm := next.(Model)

	if len(calls) != 0 {
		t.Fatalf("calls = %#v, want none — a non-immediate command must not run until the turn ends", calls)
	}
	if len(nm.queued) != 0 {
		t.Errorf("m.queued = %#v, want empty — a command line must never reach Lane.Steer as text", nm.queued)
	}
	if len(nm.deferredCmds) != 1 || nm.deferredCmds[0] != "/compact" {
		t.Errorf("m.deferredCmds = %#v, want [\"/compact\"]", nm.deferredCmds)
	}
}

// TestHandleSubmit_UnknownCommandRunsImmediatelyWhileBusy: a command the
// registry has never heard of gets its "Unknown command" error shown
// right away rather than silently sitting in deferredCmds until the turn
// ends (and definitely never reaches Lane.Steer as prose either).
func TestHandleSubmit_UnknownCommandRunsImmediatelyWhileBusy(t *testing.T) {
	var calls []string
	m, f := newTestModelWithBridge(t)
	m.cfg.Registry = busyTestRegistry(&calls)
	m.busy = true

	next, _ := m.handleSubmit("/frobnicate")
	nm := next.(Model)

	if len(nm.deferredCmds) != 0 {
		t.Errorf("m.deferredCmds = %#v, want empty for an unknown command", nm.deferredCmds)
	}
	if len(nm.queued) != 0 {
		t.Errorf("m.queued = %#v, want empty", nm.queued)
	}
	waitForPrinted(t, f, "Unknown command")
}

// TestDrainDeferredCommands_RunsAtTurnEnd: a deferred command left in
// m.deferredCmds by handleSubmit's busy branch actually runs, as a real
// command, once finishTurn ends the turn that busied it.
func TestDrainDeferredCommands_RunsAtTurnEnd(t *testing.T) {
	var calls []string
	m, f := newTestModelWithBridge(t)
	m.cfg.Registry = busyTestRegistry(&calls)
	m.busy = true
	m.deferredCmds = []string{"/compact"}

	nm, _ := m.finishTurn(msgTurnResult{})

	if len(calls) != 1 || calls[0] != "compact" {
		t.Fatalf("calls = %#v, want [\"compact\"] run once the turn ended", calls)
	}
	if len(nm.deferredCmds) != 0 {
		t.Errorf("m.deferredCmds = %#v, want empty after draining", nm.deferredCmds)
	}
	waitForPrinted(t, f, "compact ran")
}

// TestHandleSubmit_LeadingPathIsNotACommand regression-tests the
// companion bug: a dragged/pasted absolute path as the first thing in the
// prompt ("/Users/andrepato/Desktop/Screenshot\ …png more words") must not
// be run as a slash command (an "Unknown command: /Users" error) just
// because it starts with "/". A path to a file that actually exists, or
// one with a second "/" in it, is never a command name.
func TestHandleSubmit_LeadingPathIsNotACommand(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(existing, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls []string
	m, f := newTestModelWithBridge(t)
	m.cfg.Registry = busyTestRegistry(&calls)
	m.cfg.Cwd = dir

	line := existing + " please read this"
	next, _ := m.handleSubmit(line)
	nm := next.(Model)

	waitForPrinted(t, f, "please read this")
	printed, _ := f.snapshot()
	for _, p := range printed {
		if strings.Contains(p, "Unknown command") {
			t.Errorf("committed %q; an existing-file leading path must not be treated as a command attempt", p)
		}
	}
	if !nm.busy {
		t.Error("the line should have gone on to start a turn (busy=true), not been swallowed as a command")
	}
}

// TestLeadingTokenIsPath_RealCommandsUnaffected is the classifier's own
// unit test (imgpath.LeadingTokenIsPath has its own in internal/imgpath);
// this just pins that handleSubmit still runs a genuine, unambiguous
// slash command immediately while busy — the fix for bug #2 and the
// leading-path fix must not interact.
func TestLeadingTokenIsPath_RealCommandsUnaffected(t *testing.T) {
	var calls []string
	m, _ := newTestModelWithBridge(t)
	m.cfg.Registry = busyTestRegistry(&calls)
	m.busy = true

	next, _ := m.handleSubmit("/status")
	nm := next.(Model)
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want [\"status\"]", calls)
	}
	if len(nm.deferredCmds) != 0 || len(nm.queued) != 0 {
		t.Errorf("deferredCmds=%#v queued=%#v, want both empty", nm.deferredCmds, nm.queued)
	}
}

// TestPasteAsImage_WholePasteAttachesAndShowsPlaceholder regression-tests
// fix #1's whole-paste case: a terminal delivering a dragged macOS
// screenshot as one bracketed paste — backslash-escaped spaces, a U+202F
// NARROW NO-BREAK SPACE before "AM" — attaches as an image, and the
// editor shows "[Image #1]" rather than the raw escaped path (which the
// model previously retyped with a normal space, so the read failed).
func TestPasteAsImage_WholePasteAttachesAndShowsPlaceholder(t *testing.T) {
	dir := t.TempDir()
	narrow := " "
	name := "Screenshot 2026-10-02 at 11.43.28" + narrow + "AM.png"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("fake-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	escaped := strings.ReplaceAll(path, " ", `\ `)

	m, _ := newTestModelWithBridge(t)
	m.cfg.Cwd = dir
	m.editor.Focus()

	next, _ := m.Update(tea.PasteMsg{Content: escaped})
	nm := next.(Model)

	if len(nm.pendingImages) != 1 {
		t.Fatalf("pendingImages = %d, want 1", len(nm.pendingImages))
	}
	if nm.pendingImages[0].MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", nm.pendingImages[0].MimeType)
	}
	if got := nm.editor.Value(); got != "[Image #1]" {
		t.Errorf("editor.Value() = %q, want the placeholder, not the raw escaped path", got)
	}
}

// TestPasteAsImage_OrdinaryPasteUntouched checks the negative: ordinary
// pasted text (or a path to something that is not an image, or does not
// exist) is pasted exactly as before — the whole-paste image path is not
// taken just because the text starts with "/".
func TestPasteAsImage_OrdinaryPasteUntouched(t *testing.T) {
	m, _ := newTestModelWithBridge(t)
	m.editor.Focus()

	next, _ := m.Update(tea.PasteMsg{Content: "just some pasted text"})
	nm := next.(Model)

	if len(nm.pendingImages) != 0 {
		t.Errorf("pendingImages = %d, want 0 for ordinary text", len(nm.pendingImages))
	}
	if got := nm.editor.Value(); got != "just some pasted text" {
		t.Errorf("editor.Value() = %q, want the pasted text unchanged", got)
	}
}

// TestHandleSubmit_EmbeddedImagePathBecomesPlaceholder regression-tests
// fix #1's second case: a dragged/pasted path among other words in the
// same submitted line ("Add the kiln <path> header to the readme…") is
// resolved and replaced with "[Image #N]" at submit time, in both the
// echoed transcript line and what's sent on as the prompt.
func TestHandleSubmit_EmbeddedImagePathBecomesPlaceholder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, []byte("fake-png"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, f := newTestModelWithBridge(t)
	m.cfg.Cwd = dir

	line := "Add the kiln " + path + " header to the readme"
	m.handleSubmit(line)

	waitForPrinted(t, f, "[Image #1]")
	printed, _ := f.snapshot()
	for _, p := range printed {
		if strings.Contains(p, path) {
			t.Errorf("committed %q; the raw path must not appear once it resolved to an image", p)
		}
	}
}
