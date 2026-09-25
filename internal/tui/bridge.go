package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
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
	mu       sync.Mutex
	progSink sink

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
}

// bridgeItem is one entry on the commit queue: either a block of text to
// print, or — when mode is non-nil — a request to flip the commit sink
// between native scrollback (tea.Println) and the fullscreen transcript
// buffer (MsgTranscriptAppend). The flip is queued exactly like a text
// commit so it is ordered relative to every Commit call already enqueued:
// text committed before a SetFullscreen call still lands on the old sink,
// even though both are drained by the same goroutine after the flip is
// requested.
type bridgeItem struct {
	text string
	mode *bool
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
func (b *Bridge) Commit(lines []string) {
	if len(lines) == 0 {
		return
	}
	select {
	case b.queue <- bridgeItem{text: strings.Join(lines, "\n")}:
	case <-b.quit:
	}
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

// CommitCommandResult commits a slash command's result row(s) under its
// own echo: "  ⎿  <line>" for the first line, two-space continuation for
// the rest (docs/claude-code-reference.md §3: "❯ /model" / "  ⎿  Kept
// model as Opus 5 (1M context)"). The echo itself is committed separately
// via RenderUserMessage, same as a typed prompt — this only adds the
// result row(s) that follow it.
func (b *Bridge) CommitCommandResult(lines []string) {
	if len(lines) == 0 {
		return
	}
	gl := G()
	out := make([]string, len(lines))
	for i, l := range lines {
		if i == 0 {
			out[i] = fmt.Sprintf("%s%s  %s", resultIndent, TranscriptDim(gl.Result), l)
		} else {
			out[i] = continuationIndent + l
		}
	}
	b.Commit(out)
}

// Send delivers msg to the program's Update loop. tea.Program.Send already
// selects on the program's own context, so unlike Commit it needs no
// separate guard.
func (b *Bridge) Send(m tea.Msg) {
	if p := b.prog(); p != nil {
		p.Send(m)
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

// --- wiring the harness's event bus -------------------------------------

// turnState is the live state app.ts kept as closures over runApp's local
// variables (pending tool map, streamed text, thinking view, turn
// counters); the bridge keeps the same state itself since it, not app.go,
// owns the handler that mutates it.
type turnState struct {
	streamed        strings.Builder
	toolCallsInTurn int
}

// Wire subscribes to started's event bus and returns an unsubscribe func.
// tier is used to compute the tool-output truncation budget
// (max(3, ToolOutputTokens/400), app.ts's own formula).
func (b *Bridge) Wire(started *agent.Started, toolOutputTokens int) func() {
	b.ts = &turnState{}
	events := started.Harness.Events()

	unsub := events.OnAll(func(ev harness.Event) {
		b.handleEvent(ev, b.ts, toolOutputTokens)
	})
	return unsub
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
	}
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
	case harness.EventMessageUpdate:
		b.handleStreamEvent(ev.StreamEvent, ts)

	case harness.EventMessageEnd:
		if ev.Message == nil || ev.Message.Role != msg.RoleAssistant {
			return
		}
		text := assistantText(ev.Message)
		if text == "" {
			return
		}
		b.Send(msgCommitMarkdown{Text: text})

	case harness.EventToolStart:
		ts.toolCallsInTurn++

	case harness.EventToolEnd:
		name := MapToolName(ev.ToolName)
		primary := PrimaryArg(ev.ToolArgs)
		summary := summarizeToolResult(ev.ToolResult)
		max := toolOutputTokens / 400
		if max < 3 {
			max = 3
		}
		view := ToolCallView{
			Name:          name,
			PrimaryArg:    primary,
			Status:        CallOK,
			ResultLines:   clipTo(summary, max),
			TotalLines:    len(summary),
			HasTotalLines: true,
		}
		if ev.ToolResult != nil && ev.ToolResult.IsError {
			view.Status = CallError
		}
		// Edit's result renders as a diff, not a text summary — matching
		// docs/claude-code-reference.md §3's "Added N lines, removed M
		// lines" row. internal/tools/edit.go attaches a real unified
		// patch (editDetails.Patch, generateUnifiedPatch) as the tool
		// result's Details; ParseUnifiedDiff reads it directly rather
		// than reconstructing a diff from the edit's raw old/new
		// arguments, so line numbers come from the actual file content.
		if strings.EqualFold(ev.ToolName, "edit") && view.Status == CallOK && ev.ToolResult != nil {
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
		}
		// Rendered on the Update goroutine (like markdown) so result lines
		// are fitted to the live width, and so tool calls and assistant
		// text commit in the order they were sent.
		b.Send(msgCommitToolCall{View: view})
		if ev.ToolName == "exit_plan_mode" {
			// Approving a plan moves the gate's mode; the footer re-reads it.
			b.Send(MsgRefreshMode{})
		}

	case harness.EventUsage:
		var contextUsed *int
		var cost *float64
		var tokens *int
		if ev.UsageRow != nil {
			v := ev.UsageRow.Input + ev.UsageRow.Output
			contextUsed = &v
		}
		if ev.UsageTotals != nil {
			t := ev.UsageTotals.Input + ev.UsageTotals.Output
			tokens = &t
			c := ev.UsageTotals.Cost.Total
			cost = &c
		}
		b.Send(MsgUsage{ContextUsed: contextUsed, Cost: cost, Tokens: tokens})

	case harness.EventFault:
		msg := "unknown fault"
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		b.Commit(RenderError(msg))
	}
}

// msgCommitMarkdown asks the app to render assistant markdown at the
// current width and commit it. Rendering happens on the Update goroutine
// (not here) because it needs the live terminal width, which only app.go
// tracks.
type msgCommitMarkdown struct{ Text string }

// msgCommitToolCall asks the app to render a finished tool call at the
// current width and commit it.
type msgCommitToolCall struct{ View ToolCallView }

// MsgFooterNote sets the footer's transient note ("mcp: connecting 11
// servers…"); an empty Text clears it. Sent by the CLI for work that
// continues after the TUI is up.
type MsgFooterNote struct{ Text string }

// MsgRefreshMode asks the app to re-read the gate's permission mode into
// the footer, after something other than Shift+Tab changed it (a plan
// approval). Mirrors app.ts's refreshStatus() on approve.
type MsgRefreshMode struct{}

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
		tokens := compaction.EstimateTokens(msg.AssistantMessage{
			Role:    msg.RoleAssistant,
			Content: msg.Blocks{msg.Text(ts.streamed.String())},
		})
		b.Send(MsgTokens{Tokens: tokens})
	}
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
				Args:             req.Args,
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
func (b *Bridge) SubagentSink() func(agent.SubagentEvent) {
	return func(e agent.SubagentEvent) {
		switch e.Kind {
		case agent.SubagentEventStart:
			note := ""
			if e.Inherited {
				note = " " + Dim("(inherited; the requested model is not on this provider)")
			}
			model := e.ModelID
			if e.ProviderID != "" {
				model = e.ProviderID + "/" + e.ModelID
			}
			if e.ModelKind != "" {
				model += Dim(" [" + e.ModelKind + "]")
			}
			b.Commit([]string{fmt.Sprintf("%s %s %s %s %s%s", Dim(G().Call), Bold(e.Agent), Dim(e.Description), Dim("on"), model, note)})
		case agent.SubagentEventDone:
			b.Commit([]string{Dim(fmt.Sprintf("  %s finished - %d tool calls, %d chars returned, %d tokens", e.Agent, e.ToolCalls, e.Chars, e.Usage.TotalTokens))})
		case agent.SubagentEventError:
			b.Commit(RenderError(fmt.Sprintf("%s: %s", e.Agent, e.Message)))
		}
	}
}

// HookNotice renders a hook activity line, matching app.ts's onHookNotices
// (app.ts:634-637): a dim aside the user needs to see; the model does not.
func (b *Bridge) HookNotice(message string) {
	b.Commit([]string{Dim("  hook: " + message)})
}

// ModelSwitch renders the model-switch transcript line, matching app.ts's
// onModelChanges (app.ts:619-632).
func (b *Bridge) ModelSwitch(label string, tierName string, usable int) {
	b.Commit([]string{fmt.Sprintf("%s %s %s %s %s", Dim(G().Call), Bold("model"), Dim("→"), label, Dim(fmt.Sprintf("(%s tier, %s usable)", tierName, FormatTokens(usable))))})
	b.Send(MsgModelInfo{Label: label})
}

// --- helpers -------------------------------------------------------------

func titleCase(name string) string {
	if name == "" {
		return name
	}
	r := []rune(name)
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
