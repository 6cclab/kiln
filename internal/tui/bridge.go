package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tools"
)

// Bridge is the agent side of the TUI: it turns harness.Events, and every
// other source of a committed transcript block (user echo, `!`/`#` output,
// command output, turn summaries...), into calls on one single background
// goroutine that owns Program.Println.
//
// That single goroutine is the load-bearing part, for two separate
// reasons:
//
//  1. Ordering. Commit is called from at least three different
//     goroutines — the harness event bus (the lane's Prompt goroutine),
//     Bubbletea's Update (user echo, mode/command output, turn summaries),
//     and one-off Cmd goroutines — and several tea.Println Cmds fired back
//     to back are documented (phase 0) to land out of order, because each
//     runs on its own goroutine racing to send on the program's raw
//     channel. Routing every commit through one queue, drained by one
//     goroutine, makes the commit order equal the enqueue order regardless
//     of which caller's goroutine got there first.
//  2. Deadlock avoidance. Program.Println sends on the program's
//     unbuffered message channel, which only the event loop drains, and
//     only between Update calls. Calling it — or blocking on it —
//     directly from inside Update would deadlock the program the instant
//     anything tries to commit as a direct result of a keypress (this
//     was caught the hard way: see the phase report). Commit only ever
//     enqueues; the actual Println call happens on run's own goroutine,
//     never on the caller's.
//
// sink is the subset of *tea.Program the bridge's committer goroutine
// needs. It exists so a test can inject a recorder in place of a real
// *tea.Program (which needs a live event loop) — *tea.Program satisfies it
// via its existing Println/Send methods, no adapter required.
type sink interface {
	Println(...any)
	Send(tea.Msg)
}

type Bridge struct {
	// lastFault is the last EventFault message committed as an error
	// block; see FaultCommitted.
	lastFault string
	// lastWasNote reports that the most recent commit was a system note,
	// so the next note joins its block (CommitNote).
	lastWasNote bool
	mu          sync.Mutex
	progSink    sink
	// covered holds the task calls the subagents panel showed (MarkCoveredCall); guarded by mu.
	covered map[string]bool

	queue chan bridgeItem
	quit  chan struct{}
	once  sync.Once

	cwd string
	// ts is the live turn state Wire's handler mutates. See
	// ResetTurnCounters for why this needs no lock.
	ts *turnState

	// verboseMu guards verbose, the only piece of Bridge state the app's
	// Update goroutine and Wire's event-bus goroutine both read (Update on
	// Ctrl+O, the event handler when deciding how to render a tool call).
	verboseMu sync.Mutex
	verbose   bool

	// fsMu guards fullscreen, the commit sink's current mode. It is written
	// only by run() (the committer goroutine), when it drains a mode-flip
	// item off the queue, and read by printNow (same goroutine, no lock
	// needed there in principle, but taking it anyway keeps every access
	// uniform) and by Fullscreen (any goroutine).
	fsMu       sync.Mutex
	fullscreen bool

	// synthMu guards synthetics and lastEntryID: lastEntryID is written by
	// handleEvent's EventEntryAdded case (the lane's own goroutine) and
	// read by CommitSynthetic (the app's Update goroutine, e.g. Ctrl+O/
	// plan/note/command-result/subagent commit call sites); synthetics is
	// appended to by CommitSynthetic and read by Synthetics (the replay
	// path). See SyntheticCommit's doc comment.
	synthMu     sync.Mutex
	synthetics  []SyntheticCommit
	lastEntryID string

	// freezeHookMu guards freezeHook and lastFreezeCommit: freezeHook is set
	// once by app.go (NewModel, via SetFreezeHook) and read from any
	// goroutine that commits — see live_freeze.go's doc comment for the
	// whole mechanism this is one half of. lastFreezeCommit is
	// FreezeBefore's own de-duplication guard; see its doc comment.
	freezeHookMu     sync.Mutex
	freezeHook       func() []string
	lastFreezeCommit string
}

// bridgeItem is one entry on the commit queue: either a block of text to
// print, or — when mode is non-nil — a request to flip the commit sink
// between native scrollback (tea.Println) and the fullscreen transcript
// buffer (MsgTranscriptAppend), or — when msg is non-nil — a tea.Msg to
// deliver via Program.Send from the committer goroutine (see SendAsync).
// The flip is queued exactly like a text commit so it is ordered relative
// to every Commit call already enqueued: text committed before a
// SetFullscreen call still lands on the old sink, even though both are
// drained by the same goroutine after the flip is requested.
type bridgeItem struct {
	text string
	mode *bool
	msg  tea.Msg
}

// commitQueueSize is generous relative to actual traffic (keystrokes and
// harness events, not a hot loop); it exists so Commit's enqueue never has
// a reason to block under normal operation, only under runaway output.
const commitQueueSize = 1024

// NewBridge builds a Bridge with no program yet and starts its committer
// goroutine. Call SetProgram once a program exists, before it starts
// running (RunInteractive does this).
func NewBridge(cwd string) *Bridge {
	b := &Bridge{
		queue: make(chan bridgeItem, commitQueueSize),
		quit:  make(chan struct{}),
		cwd:   cwd,
	}
	go b.run()
	return b
}

// run is the bridge's single committer goroutine: it owns Program.Println,
// so nothing else ever needs a lock around calling it.
func (b *Bridge) run() {
	for {
		select {
		case item := <-b.queue:
			if item.mode != nil {
				b.fsMu.Lock()
				b.fullscreen = *item.mode
				b.fsMu.Unlock()
				continue
			}
			if item.msg != nil {
				if p := b.prog(); p != nil {
					p.Send(item.msg)
				}
				continue
			}
			b.printNow(item.text)
		case <-b.quit:
			return
		}
	}
}

// printNow runs on the run goroutine, so calling p.Send here (the
// fullscreen sink) cannot deadlock the Update loop the way calling it
// directly from Update would: Send only blocks until the event loop picks
// the message up, and this goroutine is never the one running Update.
func (b *Bridge) printNow(text string) {
	p := b.prog()
	if p == nil {
		return
	}
	b.fsMu.Lock()
	fs := b.fullscreen
	b.fsMu.Unlock()
	if fs {
		p.Send(MsgTranscriptAppend{Text: text})
	} else {
		p.Println(text)
	}
}

// SetProgram attaches the Bubbletea program. Must be called before the
// program starts receiving events that reach the bridge.
func (b *Bridge) SetProgram(p *tea.Program) {
	b.mu.Lock()
	b.progSink = p
	b.mu.Unlock()
}

// setSink is SetProgram's test-only counterpart: it accepts any sink, not
// just a real *tea.Program, so a test can inject a recorder.
func (b *Bridge) setSink(s sink) {
	b.mu.Lock()
	b.progSink = s
	b.mu.Unlock()
}

// SetFullscreen enqueues a request to switch the commit sink between
// native scrollback and the fullscreen transcript buffer. Like Commit, it
// only enqueues — the actual flag flip happens on run's own goroutine when
// it drains the item — so it is ordered relative to every other Commit
// call and safe to call from Update. Calling it before SetProgram (at
// startup) is fine: the flip item just sets the flag once drained, with no
// program to print through yet.
func (b *Bridge) SetFullscreen(v bool) {
	select {
	case b.queue <- bridgeItem{mode: &v}:
	case <-b.quit:
	}
}

// Fullscreen reports the bridge's current commit-sink mode.
func (b *Bridge) Fullscreen() bool {
	b.fsMu.Lock()
	defer b.fsMu.Unlock()
	return b.fullscreen
}

// Stop shuts the bridge's committer goroutine down. Idempotent. Anything
// still queued when it fires is dropped rather than printed after the
// program has exited.
func (b *Bridge) Stop() {
	b.once.Do(func() { close(b.quit) })
}

func (b *Bridge) prog() sink {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.progSink
}

// Commit enqueues lines to be printed to the transcript, in order,
// relative to every other Commit call from any goroutine. Safe to call
// from Bubbletea's Update itself — unlike calling Program.Println
// directly, this never blocks waiting for the event loop, so it cannot
// deadlock it.
//
// This is the transcript's one true sink: every commit path in this
// package — app.go's m.commit/m.commitSynthetic, CommitNote,
// CommitCommandResult, the freeze hook (live_freeze.go), this file's own
// event-driven commits (EventFault, SubagentSink, HookNotice, ModelSwitch)
// — reaches a terminal row only through here or through CommitSynthetic,
// which itself calls this. That makes it the one place to apply the left
// margin (layout_margin.go's padMargin, finding no-side-margin) rather than
// each of those padding its own lines: CommitSynthetic also stores its
// lines (unpadded) for RenderTranscriptEntries to splice back into a later
// Ctrl+O/Ctrl+F/Rewind replay, and that replay commits the whole redrawn
// transcript through this same Commit — padding anywhere upstream of here
// would double the margin on every synthetic block once replayed.
func (b *Bridge) Commit(lines []string) {
	if len(lines) == 0 {
		return
	}
	lines = padMargin(lines, ruleMargin())
	b.mu.Lock()
	b.lastWasNote = false
	b.mu.Unlock()
	select {
	case b.queue <- bridgeItem{text: strings.Join(lines, "\n")}:
	case <-b.quit:
	}
}

// SyntheticCommit is one non-session-entry block committed via
// CommitSynthetic — a plan checklist, a subagent dispatch line, a system
// note, or a /context block — recorded with the id of the session entry it
// was committed after (AfterEntryID; "" means before the first entry).
// RenderTranscriptEntries splices these back in after a Ctrl+O/Ctrl+F
// replay, which otherwise only knows about entries actually written to the
// session log.
type SyntheticCommit struct {
	AfterEntryID string
	Lines        []string
}

// MarkCoveredCall records a task call whose outcome the subagents panel
// shows instead of a task block, so a replay leaves it out too.
func (b *Bridge) MarkCoveredCall(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.covered == nil {
		b.covered = map[string]bool{}
	}
	b.covered[id] = true
}

// CoveredCalls is a copy of the calls MarkCoveredCall recorded.
func (b *Bridge) CoveredCalls() map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]bool, len(b.covered))
	for id := range b.covered {
		out[id] = true
	}
	return out
}

// CommitSynthetic commits lines exactly like Commit, and additionally
// records them (tagged with the entry most recently seen via
// EventEntryAdded) so a later replay can put them back in place. Every
// call site that commits a block with no session entry of its own — the
// plan checklist, subagent dispatch/done/error lines, CommitNote,
// CommitCommandResult, and the /context block — must go through this
// instead of Commit, or Ctrl+O/Ctrl+F silently drops it (RenderTranscriptEntries
// only ever sees session entries otherwise).
func (b *Bridge) CommitSynthetic(lines []string) {
	if len(lines) == 0 {
		return
	}
	b.synthMu.Lock()
	b.synthetics = append(b.synthetics, SyntheticCommit{
		AfterEntryID: b.lastEntryID,
		Lines:        append([]string(nil), lines...),
	})
	b.synthMu.Unlock()
	b.Commit(lines)
}

// Synthetics returns every synthetic commit recorded so far, in commit
// order.
func (b *Bridge) Synthetics() []SyntheticCommit {
	b.synthMu.Lock()
	defer b.synthMu.Unlock()
	return append([]SyntheticCommit(nil), b.synthetics...)
}

// SetFreezeHook registers the "live while last" freeze hook (live_freeze.go):
// a func that freezes whichever of the plan checklist / subagents panel is
// still live and returns its commit-ready lines (nil if neither is live).
// Called once by app.go's NewModel. Safe to call from any goroutine that
// later calls FreezeBefore.
func (b *Bridge) SetFreezeHook(hook func() []string) {
	b.freezeHookMu.Lock()
	b.freezeHook = hook
	b.freezeHookMu.Unlock()
}

// FreezeBefore commits whatever the freeze hook reports is still live —
// the plan checklist and/or the subagents panel — ahead of a commit the
// caller is about to make of its own. A no-op when no hook is registered
// or nothing is live. Safe to call from any goroutine (it only ever calls
// CommitSynthetic, exactly like any other commit call site).
//
// app.go's m.commit/m.commitSynthetic/m.commitNote helpers call this
// before every ordinary transcript commit; this file's own event-driven
// commits (EventFault, SubagentSink, HookNotice, ModelSwitch — the ones
// that do not run on the Update goroutine and so cannot call an app.go
// helper) call it directly. finishTurn is the one call site that
// deliberately does NOT call this ahead of its InFlightTools abort loop —
// see live_freeze.go's doc comment for why.
//
// De-duplication: SubagentPanelState.Apply (subagents.go) clears the
// panel's frozen flag on every agent.SubagentEvent, by design, so a still-
// running dispatch makes an already-frozen panel live again — see its doc
// comment. A live turn commits constantly (tool calls, notes, a permission
// decline), and every one of those calls FreezeBefore first, so the panel
// can be frozen, unfrozen by an unrelated in-flight subagent tick, and
// frozen again — and committed a second time — before its visible content
// (the header's "N/M done", the row summaries) has actually changed. A
// real session showed exactly this: the subagents panel committed, then a
// "✕ Declined …" note, then the identical panel content committed again,
// while the busy line kept running live underneath. Comparing this call's
// hook() output against the last one actually committed catches that: an
// unfrozen-then-refrozen panel whose rendered lines haven't moved produces
// no second block, while a panel that genuinely changed (a row finished, a
// count moved) still commits normally.
func (b *Bridge) FreezeBefore() {
	b.freezeHookMu.Lock()
	hook := b.freezeHook
	b.freezeHookMu.Unlock()
	if hook == nil {
		return
	}
	lines := hook()
	if len(lines) == 0 {
		return
	}
	joined := strings.Join(lines, "\n")
	b.freezeHookMu.Lock()
	dup := joined == b.lastFreezeCommit
	if !dup {
		b.lastFreezeCommit = joined
	}
	b.freezeHookMu.Unlock()
	if dup {
		return
	}
	b.CommitSynthetic(lines)
}

// SetVerbose sets the bridge's verbose-transcript flag. Called by the app
// on Ctrl+O (app.go owns the key binding; this is its side of the
// interface). Verbose mode changes how EventToolEnd renders going forward
// — it does not, by itself, repaint anything already committed, because
// committed lines are in scrollback and cannot be repainted. The app is
// responsible for the repaint: on toggle it must send MsgClearAndReplay
// (defined below) after calling SetVerbose, so the whole transcript-so-far
// commits fresh in the new mode. See MsgClearAndReplay's doc comment for
// exactly what the app must do with it.
func (b *Bridge) SetVerbose(v bool) {
	b.verboseMu.Lock()
	b.verbose = v
	b.verboseMu.Unlock()
}

// Verbose reports the bridge's current verbose-transcript flag.
func (b *Bridge) Verbose() bool {
	b.verboseMu.Lock()
	defer b.verboseMu.Unlock()
	return b.verbose
}

// MsgClearAndReplay asks the app to clear the screen and re-commit the
// transcript so far in the bridge's new verbose/collapsed mode.
//
// Sent by the app itself around its Ctrl+O handler (Bridge has no access
// to the session's own entry log — app.go/its Config does), in this order:
// call Bridge.SetVerbose(newValue), then return tea.ClearScreen as the
// handler's Cmd, then — once the clear has taken effect — rebuild every
// committed block from the session's entries (the same shape
// transcriptview.go's buildTranscriptLines used to walk, now rendered with
// RenderToolCall/RenderAssistantText/RenderUserMessage at the new
// verbosity) and Commit them in order. This message type exists so that
// contract is documented in one place; Bridge does not send or handle it
// itself, since it owns no session data and no screen — only the app can
// do the actual clear-and-replay. It is included here as the agreed shape
// for S1's Ctrl+O handler to send to itself (or simply inline the
// sequence above without a message at all — a message is only needed if
// the clear must happen asynchronously relative to the keypress).
type MsgClearAndReplay struct{}

// MsgTranscriptAppend carries one committed block's text to the app when
// the bridge's commit sink is switched to fullscreen (Bridge.SetFullscreen):
// in the alt screen there is no native scrollback for tea.Println to
// scroll into, so a fullscreen commit instead lands here and the app
// appends it to its own transcript buffer, rendered by the viewport (see
// app.go's appendTranscript). Text may itself contain embedded newlines
// (Commit joins its lines with "\n" before enqueueing), so the app splits
// on "\n" before appending.
type MsgTranscriptAppend struct{ Text string }

// RenderCommandResult renders a slash command's multi-line result as a
// labelled block named after the command ("status ───", "cost ───", …),
// every row starting at the same column so the key/value
// columns the command itself already aligned (e.g. /status's
// "model     faux/faux-1", "auth      configured") stay aligned instead of
// zig-zagging (qa/findings/20260927T000712Z-command-output-elbow-
// misaligned.json: the old "⎿ " form only ever prefixed the first row, not
// the rest, which pushed everything after it one glyph-and-two-spaces
// narrower). There is no "⎿" anywhere in kiln's block set — a one-line
// result is a "system" note instead (app.go's handleSubmit, commitNote),
// which this function does not handle.
func RenderCommandResult(name string, lines []string, width int) []string {
	label := name
	if label == "" {
		label = "result"
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, labelRule(label, Muted, "", width))
	// Rows start at the block's own margin, like a reply's text or
	// /context's legend under their label rules; the command's own
	// columns (key/value pairs, nested lists) carry through unchanged.
	for _, l := range lines {
		out = append(out, FitStatus(l, width))
	}
	return out
}

// CommitCommandResult commits a slash command's multi-line result as its
// own labelled block (RenderCommandResult), named after the command that
// produced it. The echo itself is committed separately via
// RenderUserMessage, same as a typed prompt — this only adds the block
// that follows it, with the same leading blank row every other commit
// call site in this file uses to separate blocks.
func (b *Bridge) CommitCommandResult(name string, lines []string) {
	if len(lines) == 0 {
		return
	}
	width := ruleWidth()
	out := append([]string{""}, RenderCommandResult(name, lines, width)...)
	b.CommitSynthetic(out)
}

// Send delivers msg to the program's Update loop. tea.Program.Send already
// selects on the program's own context, so unlike Commit it needs no
// separate guard.
//
// Send must never be called from the goroutine currently running the
// program's own Update (i.e. synchronously, as a direct or indirect
// result of a keypress) — tea.Program.Send blocks until the event loop's
// own goroutine drains its message channel, and if that goroutine is the
// very one calling Send, it can never get back around to do so: a
// self-deadlock. Every regular harness.Event handler in Wire is safe
// because it always runs on the lane's own background goroutine, never
// the TUI's — except Lane.Steer, the one harness method app.go calls
// synchronously from handleSubmit, which is why EventQueueUpdate's own
// handler below uses SendAsync instead of this method (a real deadlock
// this exact call once produced end to end, driving a real keypress
// through the real TUI — see test/e2e/tui_queue_test.go). A caller unsure
// which goroutine it is on should use SendAsync.
func (b *Bridge) Send(m tea.Msg) {
	if p := b.prog(); p != nil {
		p.Send(m)
	}
}

// SendAsync queues msg to be delivered via Program.Send from the bridge's
// own committer goroutine (run(), the same one Commit already uses for
// Println), never the caller's — safe to call from inside the Update
// goroutine itself, unlike Send. See Send's doc comment for why that
// distinction is load-bearing.
func (b *Bridge) SendAsync(m tea.Msg) {
	select {
	case b.queue <- bridgeItem{msg: m}:
	case <-b.quit:
	}
}

// --- tea.Msg types the bridge sends -------------------------------------

// MsgTurnBegin marks a turn's start; MsgTurnEnd its end. Sent around the
// lane.Prompt call, matching app.ts's beginTurn/endTurn.
type MsgTurnBegin struct{ Seed int }
type MsgTurnEnd struct{}

// MsgThinking carries a live reasoning block's state while it streams.
type MsgThinking struct {
	Active bool
	Text   string
	// Ended is set once, on thinking_end, so the app can commit the block
	// and clear the live view.
	Ended bool
}

// MsgTokens updates the spinner's live token estimate from streamed text.
type MsgTokens struct{ Tokens int }

// MsgUsage carries context-used and cumulative cost for the footer,
// matching app.ts's usage handler: ContextUsed comes from the LAST
// request's input+output, never the running total.
type MsgUsage struct {
	ContextUsed *int
	Cost        *float64
	// Tokens, if set, replaces the spinner's live token count from the
	// cumulative totals (input+output), same as app.ts.
	Tokens *int
}

// MsgModelInfo is sent on a model switch.
type MsgModelInfo struct {
	Label         string
	ContextWindow int
	// ContextUsed is the conversation's size as the new model will be
	// sent it (harness.Lane.ContextTokens), when known: the meter must not
	// keep the outgoing model's measured figure over the new window.
	ContextUsed *int
}

// MsgGitStatus carries a freshly read git status.
type MsgGitStatus struct{ Status GitStatus }

// MsgPermissionPrompt asks the app to show a tool-permission prompt. Reply
// is answered by the app once the user picks; the gate's own goroutine
// blocks on it.
type MsgPermissionPrompt struct {
	Request PermissionRequest
	Reply   chan PromptChoice
}

// MsgPlanPrompt asks the app to show a plan-approval prompt.
type MsgPlanPrompt struct {
	Plan  string
	Reply chan PlanReply
}

// MsgAskUserPrompt asks the app to show an ask_user_question prompt.
// Reply is answered by the app once the exchange finishes (submitted or
// cancelled), releasing the approver goroutine blocked on it.
type MsgAskUserPrompt struct {
	Questions []tools.AskUserQuestion
	Reply     chan AskUserReply
}

// MsgSpinnerLabel sets a temporary busy-line label — a verb per tool
// (busyLabelForToolStart) while a tool call is in flight — app.go's
// Update applies it to the spinner (SpinnerState.SetLabel).
type MsgSpinnerLabel struct{ Text string }

// busyLabelForToolStart builds the busy-line label for a just-started
// tool call (docs/kiln-design-handoff/README.md "Interactions"): a verb
// per tool, not a blanket "Running <ToolName>" — read/glob/grep/
// tool_search/web_fetch read as "Reading …", edit/write as "Editing …"/
// "Writing …", bash as "Running <command, truncated>", task as "Running N
// subagents" (or "Running a subagent" for a lone dispatch, or when N is
// not meaningfully known), todo_write as "Planning", and everything else
// — including an MCP tool's "mcp__server__tool" name — as
// "Running <Name>".
func busyLabelForToolStart(ts *turnState, ev harness.Event) string {
	name := strings.ToLower(ev.ToolName)
	arg := busyArgDisplay(PrimaryArg(ev.ToolArgs))

	switch name {
	case "read", "glob", "grep", "tool_search", "web_fetch":
		if arg == "" {
			return "Reading"
		}
		return "Reading " + arg
	case "edit":
		if arg == "" {
			return "Editing"
		}
		return "Editing " + arg
	case "write":
		if arg == "" {
			return "Writing"
		}
		return "Writing " + arg
	case "bash":
		cmd := ""
		if ev.ToolArgs != nil {
			cmd, _ = ev.ToolArgs["command"].(string)
		}
		if cmd == "" {
			cmd = PrimaryArg(ev.ToolArgs)
		}
		return "Running " + truncateBusyArg(skipLeadingCd(cmd), 30)
	case "task":
		if ts != nil {
			ts.tasksInFlight++
			if ts.tasksInFlight > 1 {
				return fmt.Sprintf("Running %d subagents", ts.tasksInFlight)
			}
		}
		return "Running a subagent"
	case "todo_write":
		return "Planning"
	default:
		return "Running " + MapToolName(ev.ToolName)
	}
}

// busyArgDisplay is PrimaryArg's basename when the argument is a
// filesystem path, unchanged otherwise (a URL, a grep pattern, a search
// query do not have a meaningful "basename" and read better whole).
func busyArgDisplay(arg string) string {
	if arg == "" || strings.Contains(arg, "://") {
		return arg
	}
	return filepath.Base(arg)
}

// leadingCd matches a "cd <dir> &&" (or ";") prefix of a command line.
var leadingCd = regexp.MustCompile(`^\s*cd\s+('[^']*'|"[^"]*"|\S+)\s*(&&|;)\s*`)

// skipLeadingCd drops the "cd <dir> &&" prefixes from a command line for
// the busy line: models lead most commands with an absolute cd, which
// filled the 30-rune budget with a path and hid the command itself.
func skipLeadingCd(cmd string) string {
	for {
		loc := leadingCd.FindStringIndex(cmd)
		if loc == nil || loc[1] == len(cmd) {
			return cmd
		}
		cmd = cmd[loc[1]:]
	}
}

// truncateBusyArg keeps the busy line to a bounded width for a long bash
// command (design: "first 30 chars of cmd"), preferring the head — same
// reasoning as SummarizeArg in permission_render.go: the part of a command
// that matters is almost always at the front.
//
// It does not append its own "…" marker. transcript.go's RenderSpinnerLeft
// always appends exactly one trailing "…" to the whole busy-line label
// (design: sim.status + '…', Terminal.dc.html line 398) whether or not the
// label was truncated, so a marker added here as well produced a double
// ellipsis ("Running cd /private/tmp/-Us……  43s"). Composing the two
// unconditionally — one truncation cut with no marker of its own, one
// trailing ellipsis always drawn by the renderer — leaves exactly one "…"
// on screen either way.
func truncateBusyArg(s string, n int) string {
	// One line: a multi-line command (a variable assignment, then curl)
	// otherwise broke the busy row and started its second line at column 0.
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// MsgSpinnerReset returns the busy-line label to the turn's own gerund
// (SpinnerState.ResetLabel), once a tool call's phase ends and the model
// is back to generating its own text.
type MsgSpinnerReset struct{}

// MsgRetry asks the app to show the live "error" retry block
// (docs/kiln-design-handoff/README.md's "error" row): the retry loop
// (harness/turn.go) just scheduled another attempt after Delay. Attempt is
// the 0-based attempt that just failed (retry.go's RetryView.Attempt has
// the same numbering — RenderRetry displays Attempt+1, the one about to
// run).
type MsgRetry struct {
	Message     string
	Attempt     int
	MaxAttempts int
	Delay       time.Duration
}

// MsgRetryStart asks the app to clear the live retry block: the delayed
// attempt is starting now (harness's EventRetryStart). The app remembers
// the attempt number so it can commit "↺ Reconnected on attempt N" ahead of
// whatever this attempt produces, once it succeeds.
type MsgRetryStart struct{ Attempt int }

// MsgQueue carries the lane's current Steer queue length (EventQueueUpdate)
// so the busy line can show " · N queued" (spinner.go's SetQueueLen).
type MsgQueue struct{ Len int }

// --- wiring the harness's event bus -------------------------------------

// turnState is the live state app.ts kept as closures over runApp's local
// variables (pending tool map, streamed text, thinking view, turn
// counters); the bridge keeps the same state itself since it, not app.go,
// owns the handler that mutates it.
type turnState struct {
	streamed strings.Builder
	// lastContext is the context the latest request carried (cached or
	// not), the base the busy line's token figure grows from while the
	// next reply streams.
	lastContext     int
	toolCallsInTurn int
	// toolStarts records EventToolStart's wall-clock time and identity per
	// call id, so EventToolEnd can report the call's elapsed time in the
	// tool block's Meta ("approved · 4.1s", docs/kiln-design-handoff/
	// README.md's "tool" row), and so InFlightTools can report a call that
	// is still running (started but never reached EventToolEnd) when Esc
	// aborts the turn. Written and read only from handleEvent, which the
	// doc comment on ResetTurnCounters already establishes runs on a
	// single goroutine for the life of a turn, so this needs no lock
	// either. InFlightTools reads it from Update's own goroutine, but only
	// after msgTurnResult has been received — which happens only once
	// lane.Prompt (the goroutine that runs handleEvent) has already
	// returned, so there is no concurrent writer left by the time it runs,
	// and the tea.Msg channel send/receive between the two already
	// establishes the happens-before edge the read needs.
	toolStarts map[string]toolStart
	// tasksInFlight counts `task` dispatches that have started
	// (EventToolStart) but not yet ended (EventToolEnd) in the current
	// turn — read by busyLabelForToolStart for the "Running N subagents"
	// busy-line label. Written only by Wire's label subscription
	// goroutine (the same one that reads it), so it needs no lock beyond
	// the same single-writer argument ResetTurnCounters' doc comment
	// already makes for toolStarts.
	tasksInFlight int
	// abortCommitted records call ids that finishTurn's StatusAborted path
	// has already committed a synthetic CallError block for (via
	// InFlightTools, which populates it), so a same-id EventToolEnd that
	// arrives afterward — the tool's own "Command aborted" result racing
	// the abort — does not commit a second block for the same call. See
	// EventToolEnd's abortCommitted check above and InFlightTools below.
	abortCommitted map[string]bool
	// lastStreamSend throttles MsgStreamText to at most one send per
	// streamThrottle, so a fast model's per-token deltas do not flood the
	// Update loop with a render on every single one; EventMessageEnd always
	// flushes the final state regardless of the throttle (see
	// handleStreamEvent and the EventMessageEnd case above it).
	lastStreamSend time.Time
}

// streamThrottle bounds how often the live streaming caret (stream.go)
// re-renders from text deltas.
const streamThrottle = 50 * time.Millisecond

// MsgStreamText carries the assistant text streamed so far in the current
// message, throttled to streamThrottle; the app renders it live via
// stream.go's RenderStreamLive (Model.streamText, app.go's
// renderStreamLive) until the message ends and the full markdown commits.
type MsgStreamText struct{ Text string }

// Wire subscribes to started's event bus and returns an unsubscribe func.
// tier is used to compute the tool-output truncation budget
// (max(3, ToolOutputTokens/400), app.ts's own formula).
func (b *Bridge) Wire(started *agent.Started, toolOutputTokens int) func() {
	b.ts = &turnState{toolStarts: map[string]toolStart{}}
	events := started.Harness.Events()

	unsub := events.OnAll(func(ev harness.Event) {
		b.handleEvent(ev, b.ts, toolOutputTokens)
	})
	// A second, independent subscription for the busy-line label
	// (docs/kiln-design-handoff/README.md "Interactions": "Running <tool>"
	// while a tool call is in flight, back to the turn's gerund once the
	// model resumes generating text) — kept separate from handleEvent so
	// this label-only concern does not entangle with the tool-call/commit
	// logic handleEvent owns.
	unsubLabel := events.OnAll(b.labelHandler())
	return func() {
		unsub()
		unsubLabel()
	}
}

// labelHandler returns the busy-line label subscription Wire installs:
// "Running <tool>" while a tool runs, and a compaction's progress,
// including that it is retrying.
func (b *Bridge) labelHandler() func(harness.Event) {
	// retry is the note a compaction retry adds to every progress label
	// until the compaction ends, so the row keeps saying it is a retry.
	retry := ""
	return func(ev harness.Event) {
		switch ev.Type {
		case harness.EventToolStart:
			b.Send(MsgSpinnerLabel{Text: busyLabelForToolStart(b.ts, ev)})
		case harness.EventToolEnd:
			if strings.EqualFold(ev.ToolName, "task") && b.ts != nil && b.ts.tasksInFlight > 0 {
				b.ts.tasksInFlight--
			}
		case harness.EventMessageEnd:
			b.Send(MsgSpinnerReset{})
		case harness.EventCompactionStart:
			retry = ""
			b.Send(MsgCompaction{Label: compactionLabel("", 0, 0, 0, 0)})
		case harness.EventCompactionProgress:
			b.Send(MsgCompaction{Label: compactionLabel(ev.CompactionModel, ev.CompactionPart, ev.CompactionParts, ev.CompactionPromptTokens, ev.CompactionOutputTokens) + retry})
		case harness.EventCompactionRetry:
			retry = fmt.Sprintf(" · retry %d of %d", ev.Attempt, ev.MaxAttempts)
			b.Send(MsgCompaction{Label: compactionRetryLabel(ev.CompactionPart, ev.RetryError)})
		case harness.EventCompactionEnd:
			retry = ""
			b.Send(MsgCompaction{Done: true})
		}
	}
}

// ResetTurnCounters zeroes the per-turn counters (tool calls, streamed
// text) right before starting a new turn. Safe without a lock: it and
// ToolCallsInTurn are always called from the same goroutine that runs
// lane.Prompt, which is also the goroutine every harness.Event handler in
// handleEvent runs on for the duration of that call (Events.Emit is
// synchronous), so there is never a second writer.
func (b *Bridge) ResetTurnCounters() {
	if b.ts != nil {
		b.ts.toolCallsInTurn = 0
		b.ts.streamed.Reset()
		b.ts.toolStarts = map[string]toolStart{}
		b.ts.abortCommitted = nil
		b.ts.tasksInFlight = 0
		b.ts.lastStreamSend = time.Time{}
	}
	// A new turn's plan/subagents state starts fresh (app.go's beginTurn
	// resets both), so FreezeBefore's de-duplication guard must not compare
	// this turn's first freeze against whatever the previous turn last
	// froze — otherwise a coincidentally identical first block (e.g. the
	// same single "Running a subagent" summary) would be silently dropped.
	b.freezeHookMu.Lock()
	b.lastFreezeCommit = ""
	b.freezeHookMu.Unlock()
}

// ToolCallsInTurn reports how many tool_start events fired since the last
// ResetTurnCounters.
func (b *Bridge) ToolCallsInTurn() int {
	if b.ts == nil {
		return 0
	}
	return b.ts.toolCallsInTurn
}

func (b *Bridge) handleEvent(ev harness.Event, ts *turnState, toolOutputTokens int) {
	switch ev.Type {
	case harness.EventEntryAdded:
		// Tags the next CommitSynthetic call (plan/note/command-result/
		// subagent-line/context-block commit) with the entry it followed,
		// so a Ctrl+O/Ctrl+F replay can splice it back into the right
		// place. EventEntryAdded fires for every entry the lane commits
		// (user prompt, assistant message, tool result), unlike
		// EventMessageEnd/EventToolEnd which carry it only for their own
		// kind, so this is the one event guaranteed to have EntryID set
		// regardless of which entry just landed.
		b.synthMu.Lock()
		b.lastEntryID = ev.EntryID
		b.synthMu.Unlock()

	case harness.EventMessageStart:
		// A turn can run several assistant messages back to back, each
		// separated by its own tool calls (turn.go emits one
		// EventMessageStart/EventMessageEnd pair per call to
		// requestWithRetry). ts.streamed used to be reset only on
		// EventThinkingStart and once per turn (ResetTurnCounters), so text
		// deltas from every message after the first kept appending onto the
		// same builder and the live region showed them all run together
		// with no separation. Resetting here means each message's live
		// stream starts from empty; the committed block for a finished
		// message never reads ts.streamed (EventMessageEnd uses
		// assistantText(ev.Message) instead), so no partial text is lost.
		ts.streamed.Reset()

	case harness.EventMessageUpdate:
		b.handleStreamEvent(ev.StreamEvent, ts)

	case harness.EventMessageEnd:
		if ev.Message == nil || ev.Message.Role != msg.RoleAssistant {
			return
		}
		text := assistantText(ev.Message)
		if text == "" && ev.Message.StopReason == msg.StopStop && !hasVisibleContent(ev.Message.Content) {
			// A turn that ended normally with nothing in it left no trace
			// on screen, which read as a dropped message.
			b.CommitNote("The model ended its turn without replying.")
			return
		}
		if text != "" {
			// The final state always sends regardless of streamThrottle —
			// a throttled-away last delta must not be the one dropped, or
			// the live caret's last frame would show stale, truncated text
			// for an instant before the committed block replaces it.
			b.Send(MsgStreamText{Text: text})
		}
		if !hasSearchBlocks(ev.Message.Content) {
			if text != "" {
				b.Send(msgCommitMarkdown{Text: text})
			}
			return
		}
		// A message that ran a web search interleaves text and search
		// blocks in Content order (Anthropic can emit text, a search, then
		// more text, all within one message since the search resolved
		// server-side mid-stream): commit each in that order instead of
		// the single whole-message markdown block above.
		b.commitMessageInOrder(ev.Message)

	case harness.EventToolStart:
		ts.toolCallsInTurn++
		if ts.toolStarts == nil {
			ts.toolStarts = map[string]toolStart{}
		}
		ts.toolStarts[ev.ToolCallID] = toolStart{Name: ev.ToolName, PrimaryArg: PrimaryArg(ev.ToolArgs), At: time.Now()}

	case harness.EventToolEnd:
		// todo_write drives the live "plan" block (plan.go), not a
		// committed tool call — docs/kiln-design-handoff/README.md's
		// "plan" row has no room for a tool block above it, and the
		// checklist already reports its own status per item.
		if strings.EqualFold(ev.ToolName, "todo_write") {
			delete(ts.toolStarts, ev.ToolCallID)
			b.Send(MsgTodos{Items: todosFromArgs(ev.ToolArgs)})
			return
		}

		// finishTurn's StatusAborted path (app.go) already committed a
		// synthetic CallError block for this call from InFlightTools()
		// before this, this call's own, real EventToolEnd was processed
		// (Esc's abort and the tool's own ctx-cancellation error race;
		// see InFlightTools' doc comment). Committing again here would
		// double the block in the transcript — skip it, but still clean
		// up toolStarts so a stale entry cannot leak into the next turn.
		// A call the user declined at the prompt is already reported by
		// the "✕ Declined …" note (app.go); its result is the
		// model-facing refusal text, which would say it a second time.
		if ev.PermissionOutcome == string(permission.OutcomeDeclined) {
			delete(ts.toolStarts, ev.ToolCallID)
			return
		}
		if ts.abortCommitted != nil && ts.abortCommitted[ev.ToolCallID] {
			delete(ts.abortCommitted, ev.ToolCallID)
			delete(ts.toolStarts, ev.ToolCallID)
			return
		}

		name := MapToolName(ev.ToolName)
		primary := DisplayArg(ev.ToolName, PrimaryArg(ev.ToolArgs), b.cwd, b.Verbose())
		summary := collapsedSummary(ev.ToolName, summarizeToolResult(ev.ToolResult), ev.ToolResult != nil && ev.ToolResult.IsError, b.Verbose())
		// The committed block shows a short output summary and a
		// "… +N lines (ctrl+o to expand)" tail (design: "tool" row);
		// verbose mode gets the tool-output budget instead.
		max := collapsedResultLines
		if b.Verbose() {
			max = toolOutputTokens / 400
			if max < collapsedResultLines {
				max = collapsedResultLines
			}
		}
		status := CallOK
		if ev.ToolResult != nil && ev.ToolResult.IsError {
			status = CallError
		}
		view := ToolCallView{
			Name:          name,
			PrimaryArg:    primary,
			Status:        status,
			ResultLines:   clipResultLines(summary, max, status),
			TotalLines:    len(summary),
			HasTotalLines: true,
			Meta:          toolMeta(ts, ev),
		}
		// Edit's and a successful Write's result render as a diff, not a
		// text summary — matching the kiln "diff" block
		// (docs/kiln-design-handoff/README.md's block table). Edit
		// (internal/tools/edit.go's editDetails) and Write
		// (internal/tools/write.go's writeDetails) both attach a real
		// unified patch as the tool result's Details; ParseUnifiedDiff
		// reads it directly rather than reconstructing a diff from the
		// call's raw arguments, so line numbers come from the actual file
		// content.
		switch {
		case strings.EqualFold(ev.ToolName, "edit") && view.Status == CallOK && ev.ToolResult != nil:
			if d := diffFromToolDetails(ev.ToolResult.Details); d != nil {
				view.Diff = d
				view.ResultLines = nil
			} else if args := ev.ToolArgs; args != nil {
				// Fallback for a result that, for whatever reason,
				// carries no Details (an older session log, a stub in
				// tests): pair the edit's own old/new text line-for-line
				// (DiffFromEdit's doc comment explains the limits of
				// that).
				if edits, ok := args["edits"].([]any); ok && len(edits) == 1 {
					if e, ok := edits[0].(map[string]any); ok {
						old, _ := e["oldText"].(string)
						next, _ := e["newText"].(string)
						view.Diff = DiffFromEdit(old, next, 1)
						view.ResultLines = nil
					}
				}
			}
		case strings.EqualFold(ev.ToolName, "write") && view.Status == CallOK && ev.ToolResult != nil:
			if d := writeDiffFromToolDetails(ev.ToolResult.Details); d != nil {
				view.Diff = d
				view.ResultLines = nil
			}
		}
		// Rendered on the Update goroutine (like markdown) so result lines
		// are fitted to the live width, and so tool calls and assistant
		// text commit in the order they were sent.
		b.Send(msgCommitToolCall{View: view, CallID: ev.ToolCallID})
		if ev.ToolName == "exit_plan_mode" {
			// Approving a plan moves the gate's mode; the footer re-reads it.
			b.Send(MsgRefreshMode{})
		}

	case harness.EventQueueUpdate:
		// SendAsync, not Send: Lane.Steer emits this event synchronously
		// from app.go's handleSubmit, on the Program's own Update
		// goroutine — a direct Send there deadlocks (see Send's doc
		// comment).
		b.SendAsync(MsgQueue{Len: ev.QueueLen})

	case harness.EventRetryScheduled:
		b.Send(MsgRetry{
			Message:     humaniseRetryError(ev.RetryError),
			Attempt:     ev.Attempt,
			MaxAttempts: ev.MaxAttempts,
			Delay:       time.Duration(ev.DelayMs) * time.Millisecond,
		})
		b.Send(MsgSpinnerLabel{Text: "Reconnecting"})

	case harness.EventRetryStart:
		b.Send(MsgRetryStart{Attempt: ev.Attempt})
		b.Send(MsgSpinnerReset{})

	case harness.EventUsage:
		var contextUsed *int
		var cost *float64
		var tokens *int
		if ev.UsageRow != nil {
			// Everything the request carried is in context, cached or not.
			// Counting only uncached input read ~0% once the conversation
			// is served from the prompt cache.
			u := ev.UsageRow
			v := u.Input + u.CacheRead + u.CacheWrite + u.Output
			contextUsed = &v
			// The busy line's figure is the same quantity (design: "58k
			// tokens" beside a 29% meter), so both read one number. It
			// used to be the session's Input+Output, which collapses to
			// tens of tokens once the conversation is cached.
			ts.lastContext = v
			tokens = &v
		}
		if ev.UsageTotals != nil {
			c := ev.UsageTotals.Cost.Total
			cost = &c
		}
		b.Send(MsgUsage{ContextUsed: contextUsed, Cost: cost, Tokens: tokens})

	case harness.EventFault:
		msg := "unknown fault"
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		// Through the app like msgCommitToolCall, not straight to Commit: a
		// tool block for a call that ran before the fault is still on its
		// way through Update, and a direct Commit overtook it, so the error
		// landed above the call that preceded it. SendAsync because a fault
		// can be emitted from the Update goroutine (see Send).
		b.SendAsync(msgCommitFault{Message: msg})
		b.mu.Lock()
		b.lastFault = msg
		b.mu.Unlock()
	}
}

// FaultCommitted reports whether the bridge already committed an error
// block for this exact message (EventFault), so finishTurn does not
// commit the same failure a second time.
func (b *Bridge) FaultCommitted(msg string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastFault == msg
}

// msgCommitMarkdown asks the app to render assistant markdown at the
// current width and commit it. Rendering happens on the Update goroutine
// (not here) because it needs the live terminal width, which only app.go
// tracks.
type msgCommitMarkdown struct{ Text string }

// msgCommitToolCall asks the app to render a finished tool call at the
// current width and commit it.
type msgCommitToolCall struct {
	View ToolCallView
	// CallID is the tool call's id, so a task call the subagents panel
	// already reports can be left out.
	CallID string
}

// faultStatus reads the HTTP status off a provider error, which
// provider/api's StatusError writes as "<Status Text> (<code>)…".
var faultStatus = regexp.MustCompile(`^[A-Z][A-Za-z '-]* \((\d{3})\)`)

// faultHint is the next step for a failed turn, by the provider's HTTP
// status: the error line says what happened, this says what to do.
func faultHint(message string) string {
	m := faultStatus.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	switch code := m[1]; {
	case code == "401" || code == "403":
		return "Check this provider's credentials: /login shows how to sign in."
	case code == "404":
		return "This provider may not offer the model: /model lists the ones it does."
	case code == "429":
		return "Still rate limited after retrying. Wait a minute, then send again."
	case code >= "500":
		return "The provider kept failing after retries. Send again shortly, or switch with /model."
	}
	return ""
}

// msgCommitFault asks the app to commit a turn's error block, in order with
// the tool calls committed before it.
type msgCommitFault struct{ Message string }

// MsgFooterNote sets the footer's transient note ("mcp: connecting 11
// servers…"); an empty Text clears it. Sent by the CLI for work that
// continues after the TUI is up.
type MsgFooterNote struct{ Text string }

// MsgRefreshMode asks the app to re-read the gate's permission mode into
// the footer, after something other than Shift+Tab changed it (a plan
// approval). Mirrors app.ts's refreshStatus() on approve.
type MsgRefreshMode struct{}

// MsgTodos carries the live "plan" block's items, sent whenever a
// todo_write call finishes (EventToolEnd). The app renders it in the live
// region (app.go's renderPlanLive) and commits the final state once, at
// turn end — see plan.go's RenderPlan.
type MsgTodos struct{ Items []TodoView }

// todosFromArgs decodes todo_write's arguments (internal/tools/todo.go's
// todoArgs: {"todos": [{"content", "status"}]}) into the []TodoView
// plan.go renders. Anything that fails to decode as expected yields an
// empty plan rather than a partial/garbled one — a missing checklist is a
// less confusing failure than a wrong one.
func todosFromArgs(args map[string]any) []TodoView {
	raw, ok := args["todos"].([]any)
	if !ok {
		return nil
	}
	items := make([]TodoView, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		content, _ := m["content"].(string)
		status, _ := m["status"].(string)
		items = append(items, TodoView{Content: content, Status: TodoStatus(status)})
	}
	return items
}

// humaniseRetryError turns the retry loop's raw error text (harness/
// turn.go's `err.Error()`, harness/events.go's Event.RetryError) into the
// live retry block's message line (docs/kiln-design-handoff/README.md's
// "error" row: "Stream interrupted · 529 overloaded"). The event carries
// only the error's string form, not the original error value, so this
// recognizes the same shapes retry.go's own isRetriable fallback does
// (status codes and the provider.StreamInterrupted wording) by pattern
// matching the string rather than an errors.As type switch — a
// same-package errors.As here would still only ever see the string that
// crossed the Event boundary, so a status-code/type check adds no more
// signal than matching the text already does.
func humaniseRetryError(raw string) string {
	if raw == "" {
		return "Request failed"
	}
	lower := strings.ToLower(raw)
	if status, reason, ok := extractStatus(lower); ok {
		return "Stream interrupted · " + status + " " + reason
	}
	if strings.Contains(lower, "stream interrupted") {
		return "Stream interrupted"
	}
	msg := raw
	if len(msg) > 60 {
		msg = msg[:60]
	}
	return "Request failed · " + msg
}

// extractStatus recognizes the handful of HTTP statuses the retry policy
// itself treats specially (retry.go's isRetriable), returning a short
// reason word for each.
func extractStatus(lower string) (status, reason string, ok bool) {
	switch {
	case strings.Contains(lower, "529"):
		return "529", "overloaded", true
	case strings.Contains(lower, "overloaded"):
		return "529", "overloaded", true
	case strings.Contains(lower, "429"):
		return "429", "rate limited", true
	}
	return "", "", false
}

// toolMeta builds the tool block's label-rule meta: the approval outcome
// (permission.Outcome, carried on the event as a plain string) and the
// call's elapsed time, joined with " · " (docs/kiln-design-handoff/
// README.md's "tool" row meta, "approved · 4.1s"). Elapsed is a real
// duration measured against EventToolStart's timestamp, except under
// HARNESS_TEST_CLOCK, where every PTY golden needs a deterministic
// screen: the elapsed segment then always reads "1.0s" instead of real
// wall-clock skew (see footer.go's clockOverride for the same override
// elsewhere in the TUI).
func toolMeta(ts *turnState, ev harness.Event) string {
	var parts []string
	switch ev.PermissionOutcome {
	case string(permission.OutcomeApproved):
		parts = append(parts, "approved")
	case string(permission.OutcomeAuto):
		parts = append(parts, "auto-approved")
	case string(permission.OutcomeHookBlocked):
		parts = append(parts, "blocked by hook")
	case string(permission.OutcomeClassifierBlocked):
		parts = append(parts, "blocked by auto mode")
	}
	if elapsed, ok := toolElapsed(ts, ev.ToolCallID); ok {
		parts = append(parts, elapsed)
	}
	return strings.Join(parts, " · ")
}

// toolElapsed reports the call's elapsed time string, or false if its
// start was never recorded (e.g. this event arrived without a matching
// EventToolStart, which should not happen in practice but must not panic
// if it does).
func toolElapsed(ts *turnState, callID string) (string, bool) {
	start, ok := ts.toolStarts[callID]
	if !ok {
		return "", false
	}
	// The entry is removed either way — a deterministic elapsed string
	// under the test clock does not exempt this call from being "no
	// longer in flight" (InFlightTools must not report it a second time).
	delete(ts.toolStarts, callID)
	if _, testClock := os.LookupEnv("HARNESS_TEST_CLOCK"); testClock {
		return "1.0s", true
	}
	// A call that took under a tenth of a second reads as "0.0s" on every
	// file read: noise, not information.
	d := time.Since(start.At)
	if d < 100*time.Millisecond {
		return "", false
	}
	return fmt.Sprintf("%.1fs", d.Seconds()), true
}

// toolStart is one call's identity and start time, recorded on
// EventToolStart and consumed by EventToolEnd (toolElapsed, which deletes
// the entry) or, for a call that never reaches EventToolEnd, read by
// InFlightTools when Esc aborts the turn.
type toolStart struct {
	Name       string
	PrimaryArg string
	At         time.Time
}

// InFlightTools reports every tool call that started (EventToolStart) but
// never finished (EventToolEnd) in the current turn — the calls Esc's
// abort cuts off mid-flight. Each renders as a CallError block with an
// elapsed-time meta, same shape as a call that failed on its own
// (docs/kiln-design-handoff/README.md's "tool" row), so an interrupted
// call is not silently dropped from the transcript.
func (b *Bridge) InFlightTools() []ToolCallView {
	if b.ts == nil {
		return nil
	}
	type idStart struct {
		id string
		st toolStart
	}
	starts := make([]idStart, 0, len(b.ts.toolStarts))
	for id, st := range b.ts.toolStarts {
		starts = append(starts, idStart{id: id, st: st})
	}
	// Deterministic order (oldest call first), not map iteration order —
	// matters for a golden with more than one in-flight call.
	sort.Slice(starts, func(i, j int) bool { return starts[i].st.At.Before(starts[j].st.At) })
	views := make([]ToolCallView, 0, len(starts))
	if b.ts.abortCommitted == nil {
		b.ts.abortCommitted = map[string]bool{}
	}
	for _, s := range starts {
		elapsed := "1.0s"
		if _, testClock := os.LookupEnv("HARNESS_TEST_CLOCK"); !testClock {
			elapsed = fmt.Sprintf("%.1fs", time.Since(s.st.At).Seconds())
		}
		views = append(views, ToolCallView{
			Name:       MapToolName(s.st.Name),
			PrimaryArg: s.st.PrimaryArg,
			Status:     CallError,
			Meta:       elapsed,
		})
		// This call's own EventToolEnd may still arrive (see doc comment):
		// remember it here so that arrival is a no-op instead of a second
		// committed block.
		b.ts.abortCommitted[s.id] = true
	}
	return views
}

// writeDiffFromToolDetails decodes a write result's Details payload
// (internal/tools/write.go's writeDetails: {newFile, patch}) and parses
// its unified Patch into a ToolDiff, same as diffFromToolDetails does for
// edit, plus NewFile for the diff block's "new file" tag.
func writeDiffFromToolDetails(details json.RawMessage) *ToolDiff {
	if len(details) == 0 {
		return nil
	}
	var v struct {
		NewFile bool   `json:"newFile"`
		Patch   string `json:"patch"`
	}
	if err := json.Unmarshal(details, &v); err != nil || v.Patch == "" {
		return nil
	}
	d := ParseUnifiedDiff(v.Patch, 1)
	d.NewFile = v.NewFile
	return d
}

func (b *Bridge) handleStreamEvent(se *msg.StreamEvent, ts *turnState) {
	if se == nil {
		return
	}
	switch se.Type {
	case msg.EventThinkingStart:
		ts.streamed.Reset()
		b.Send(MsgThinking{Active: true, Text: ""})
	case msg.EventThinkingDelta:
		b.Send(MsgThinking{Active: true, Text: se.Delta})
	case msg.EventThinkingEnd:
		b.Send(MsgThinking{Ended: true})
	case msg.EventTextDelta:
		if se.Delta == "" {
			return
		}
		ts.streamed.WriteString(se.Delta)
		tokens := ts.lastContext + compaction.EstimateTokens(msg.AssistantMessage{
			Role:    msg.RoleAssistant,
			Content: msg.Blocks{msg.Text(ts.streamed.String())},
		})
		b.Send(MsgTokens{Tokens: tokens})
		now := time.Now()
		if now.Sub(ts.lastStreamSend) >= streamThrottle {
			ts.lastStreamSend = now
			b.Send(MsgStreamText{Text: ts.streamed.String()})
		}
	case msg.EventProviderBlockEnd:
		// A server-side web search runs inside the stream, with no
		// EventToolStart of its own: without this the busy line kept the
		// turn's gerund for the seconds the search took.
		if label, ok := webSearchBusyLabel(se.Content); ok {
			b.Send(MsgSpinnerLabel{Text: label})
		} else if strings.Contains(se.Content, `"web_search_tool_result"`) {
			b.Send(MsgSpinnerReset{})
		}
	}
}

// webSearchBusyLabel is the busy label for a completed server_tool_use
// web_search block: "Searching the web for <query>".
func webSearchBusyLabel(raw string) (string, bool) {
	var b anthropicServerToolUseBlock
	if json.Unmarshal([]byte(raw), &b) != nil || b.Type != "server_tool_use" || b.Name != "web_search" {
		return "", false
	}
	var in struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(b.Input, &in)
	if in.Query == "" {
		return "Searching the web", true
	}
	return "Searching the web for " + truncateBusyArg(in.Query, 40), true
}

// --- gate / plan approver wiring ----------------------------------------

// Prompter returns a permission.Prompter that shows a MsgPermissionPrompt
// and blocks the calling goroutine (the tool-execution path inside the
// lane's turn loop) until the app answers it. The frame showing the
// question is guaranteed to have painted before this unblocks: Send only
// hands the message to the Bubbletea event loop, which applies it via
// Update and renders before it processes anything else — in particular
// before it can process the KeyPressMsg that answers the prompt.
func (b *Bridge) Prompter(cwd string) permission.Prompter {
	return func(ctx context.Context, req permission.Request) (permission.PromptChoice, error) {
		reply := make(chan PromptChoice, 1)
		b.Send(MsgPermissionPrompt{
			Request: PermissionRequest{
				ToolName:         req.ToolName,
				PrimaryArg:       req.PrimaryArg,
				OutsideWorkspace: req.OutsideWorkspace,
				Unsandboxed:      req.Unsandboxed,
				Args:             req.Args,
				Grantable:        req.Grantable,
				DontAskRules:     req.DontAskRules,
				AutoModeNote:     req.AutoModeNote,
				InAutoMode:       req.InAutoMode,
			},
			Reply: reply,
		})
		select {
		case choice := <-reply:
			return permission.PromptChoice{
				Kind:     permission.PromptKind(choice.Kind),
				Feedback: choice.Feedback,
			}, nil
		case <-ctx.Done():
			return permission.PromptChoice{}, ctx.Err()
		case <-b.quit:
			return permission.PromptChoice{Kind: permission.PromptKind(ChoiceDeny)}, nil
		}
	}
}

// PlanApprover returns an agent.PlanApprover with the same blocking
// contract as Prompter.
func (b *Bridge) PlanApprover() agent.PlanApprover {
	return func(ctx context.Context, plan string) (agent.PlanDecision, error) {
		reply := make(chan PlanReply, 1)
		b.Send(MsgPlanPrompt{Plan: plan, Reply: reply})
		select {
		case decision := <-reply:
			kind := agent.PlanDecisionRevise
			if decision.Kind == PlanApprove {
				kind = agent.PlanDecisionApprove
			}
			return agent.PlanDecision{Kind: kind, Mode: decision.Mode, Feedback: decision.Feedback}, nil
		case <-ctx.Done():
			return agent.PlanDecision{}, ctx.Err()
		case <-b.quit:
			return agent.PlanDecision{Kind: agent.PlanDecisionRevise, Feedback: "cancelled"}, nil
		}
	}
}

// AskUserApprover returns a tools.AskUserApprover with the same blocking
// contract as Prompter/PlanApprover.
func (b *Bridge) AskUserApprover() tools.AskUserApprover {
	return func(ctx context.Context, questions []tools.AskUserQuestion) ([]tools.AskUserAnswer, error) {
		reply := make(chan AskUserReply, 1)
		b.Send(MsgAskUserPrompt{Questions: questions, Reply: reply})
		select {
		case r := <-reply:
			if r.Cancelled {
				return nil, tools.ErrAskUserCancelled
			}
			return r.Answers, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-b.quit:
			return nil, tools.ErrAskUserCancelled
		}
	}
}

// MsgSubagentEvent carries one agent.SubagentEvent to the app's Update
// loop, so the subagents panel (app.go's SubagentPanelState) can track
// live dispatches the same way every other live-region piece of state is
// driven by messages rather than by reading the dispatcher directly.
type MsgSubagentEvent struct{ Event agent.SubagentEvent }

// SubagentPanelSink returns an agent.Dispatcher OnEvent handler that feeds
// the subagents panel. It is composed with SubagentSink (below) at the
// call site (internal/cli/tui.go) rather than merged into one function,
// so the transcript sink — which is stateless and Go-only-facing — stays
// independent of the panel's Model-side state.
func (b *Bridge) SubagentPanelSink() func(agent.SubagentEvent) {
	return func(e agent.SubagentEvent) {
		b.Send(MsgSubagentEvent{Event: e})
	}
}

// SubagentSink returns an agent.Dispatcher OnEvent handler that renders
// subagent progress into the transcript, matching app.ts's
// onSubagentEvents (app.ts:640-650). Subagent tool calls are never
// echoed — that would undo their context isolation visually even though
// it is real underneath — only the dispatch, the model it landed on, and
// the result size are.
//
// A subagent failure (SubagentEventError) is deliberately NOT committed
// here as its own transcript block: it already renders twice on its own —
// once in the subagents panel's row (subagents.go's renderSubagentRow,
// the "✕ <message>" action line, the design's one record of a dispatch)
// and once in the parent's own "task" tool-call result text
// (internal/tools/task.go turns a Dispatch error into the call's result,
// "Subagent %q failed: %s", rendered by the ordinary EventToolEnd path
// below). A bare "error" block committed here as well was a third,
// context-free copy of the identical message with no visible tie to
// either of those (qa/findings/20260927T000350Z-subagent-error-
// triplicated.json) — removed rather than suppressed elsewhere, since the
// panel row and the task result already say everything a failed dispatch
// needs to say. A genuine top-level fault (the parent turn's own, not a
// subagent's) still renders via handleEvent's EventFault case above,
// which this function has no bearing on.
func (b *Bridge) SubagentSink() func(agent.SubagentEvent) {
	return func(e agent.SubagentEvent) {}
}

// HookNotice renders a hook activity line, matching app.ts's onHookNotices
// (app.ts:634-637): a dim aside the user needs to see; the model does not.
//
// A PreToolUse rewrite ("rewrote bash: …") goes to the debug log, not the
// transcript: a hook that rewrites every command (a token-filtering proxy)
// otherwise put a system block above nearly every bash call, repeating the
// command the block below already shows. What a hook says itself, and a
// block, still reach the transcript.
func (b *Bridge) HookNotice(message string) {
	if strings.HasPrefix(message, "rewrote ") {
		diag.L().Info("hook rewrite", "detail", message)
		return
	}
	b.CommitNote("hook: " + message)
}

// ModelSwitch renders the model-switch transcript line, matching app.ts's
// onModelChanges (app.ts:619-632).
//
// The note goes through the app rather than straight to Commit: a switch
// made from /model's open dialog must land after the "/model" echo, which
// the app commits only once the dialog closes. SendAsync, because a
// "/model <name>" argument switch runs on the Update goroutine.
func (b *Bridge) ModelSwitch(label string, tierName string, usable int, contextUsed *int) {
	// No transcript note of its own: /model, the only thing that switches
	// models, confirms the switch itself, and a second note repeated it.
	//
	// usable is the new tier's ContextWindow (internal/cli/tui.go's call
	// site passes resolved.Tier.ContextWindow); it must reach the footer
	// or the context meter keeps the old model's window as its
	// denominator after a switch (the "% used" figure silently lies about
	// what it is a percentage of).
	b.SendAsync(MsgModelInfo{Label: label, ContextWindow: usable, ContextUsed: contextUsed})
}

// --- helpers -------------------------------------------------------------

// titleCase turns a snake_case tool name into a readable phrase: split on
// "_" (dropping empty segments, so an MCP tool's "mcp__server__tool"
// double-underscore does not leave a double space), join with a single
// space, and capitalize only the very first letter of the whole phrase —
// "bash_background" -> "Bash background", "exit_plan_mode" -> "Exit plan
// mode", "mcp__fixture__echo" -> "Mcp fixture echo" — not per-word title
// case, which would read as shouting for a multi-word tool name.
func titleCase(name string) string {
	if name == "" {
		return name
	}
	parts := strings.Split(name, "_")
	words := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			words = append(words, p)
		}
	}
	joined := strings.Join(words, " ")
	if joined == "" {
		return name
	}
	r := []rune(joined)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

func clipTo(lines []string, max int) []string {
	if len(lines) <= max {
		return lines
	}
	return lines[:max]
}

func assistantText(m *msg.AssistantMessage) string {
	var parts []string
	for _, block := range m.Content {
		if t, ok := block.(msg.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

// hasVisibleContent reports whether an assistant message holds anything
// the transcript shows besides its text: a tool call, thinking, or a
// provider block such as a web search.
func hasVisibleContent(content msg.Blocks) bool {
	for _, c := range content {
		switch cv := c.(type) {
		case msg.ToolCall, msg.ProviderBlock:
			return true
		case msg.ThinkingContent:
			if strings.TrimSpace(cv.Thinking) != "" {
				return true
			}
		}
	}
	return false
}

// hasSearchBlocks reports whether m's content carries an Anthropic
// web_search_tool_result ProviderBlock — the signal bridge.go uses to
// switch EventMessageEnd from "commit the whole message as one markdown
// block" to "walk Content in order, committing each search where it
// happened".
func hasSearchBlocks(content msg.Blocks) bool {
	for _, c := range content {
		if pb, ok := c.(msg.ProviderBlock); ok && pb.Provider == "anthropic" {
			if bytes.Contains(pb.Raw, []byte(`"web_search_tool_result"`)) {
				return true
			}
		}
	}
	return false
}

// commitMessageInOrder renders m's content in order, flushing accumulated
// text as its own markdown block whenever a web search result block is
// reached, and committing that search as a tool-call-shaped block (same
// renderer as a real tool call: label, query, "→ N results · domains").
// server_tool_use blocks carry no row of their own — their query is read
// into the paired result's PrimaryArg via searchQueriesByToolUseID.
func (b *Bridge) commitMessageInOrder(m *msg.AssistantMessage) {
	queries := searchQueriesByToolUseID(m.Content)
	var pending strings.Builder
	flush := func() {
		text := strings.TrimSpace(pending.String())
		pending.Reset()
		if text != "" {
			b.Send(msgCommitMarkdown{Text: text})
		}
	}
	for _, c := range m.Content {
		switch cv := c.(type) {
		case msg.TextContent:
			pending.WriteString(cv.Text)
		case msg.ProviderBlock:
			view, ok := searchResultView(cv, queries)
			if !ok {
				continue
			}
			flush()
			b.Send(msgCommitToolCall{View: view})
		}
	}
	flush()
}

// diffFromToolDetails decodes an edit result's Details payload
// (internal/tools/edit.go's editDetails: {diff, patch, firstChangedLine})
// and parses its unified Patch into a ToolDiff. Returns nil when Details
// is empty or carries no patch, so the caller can fall back.
func diffFromToolDetails(details json.RawMessage) *ToolDiff {
	if len(details) == 0 {
		return nil
	}
	var v struct {
		Patch            string `json:"patch"`
		FirstChangedLine int    `json:"firstChangedLine"`
	}
	if err := json.Unmarshal(details, &v); err != nil || v.Patch == "" {
		return nil
	}
	return ParseUnifiedDiff(v.Patch, v.FirstChangedLine)
}

// summarizeToolResult converts a ToolResultMessage into the shape
// Summarize (transcript.go) reads: a map with a "content" array of
// {"type":"text","text":...} blocks, by round-tripping through the same
// JSON encoding the session log itself uses.
// collapsedResultLines is how many output lines a committed tool block
// shows outside verbose mode.
const collapsedResultLines = 3

func summarizeToolResult(tr *msg.ToolResultMessage) []string {
	if tr == nil {
		return nil
	}
	b, err := json.Marshal(tr)
	if err != nil {
		return nil
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	return Summarize(v)
}
