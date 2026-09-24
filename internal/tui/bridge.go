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
//     was caught the hard way: see the phase report). Commit/SetAltScreen
//     only ever enqueue; the actual Println call happens on run's own
//     goroutine, never on the caller's.
type Bridge struct {
	mu      sync.Mutex
	program *tea.Program

	queue chan bridgeItem
	quit  chan struct{}
	once  sync.Once

	cwd string
	// ts is the live turn state Wire's handler mutates. See
	// ResetTurnCounters for why this needs no lock.
	ts *turnState
}

// bridgeItem is one entry on the commit queue: either a block of text to
// print, or an alt-screen state change. Both go through the same channel
// so they are applied in the order they actually happened, not just the
// order two separately-locked booleans happened to be read.
type bridgeItem struct {
	text        string
	setAltScren bool
	altActive   bool
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

// run is the bridge's single committer goroutine: it owns Program.Println
// and the alt-screen queueing state, so neither ever needs a lock.
func (b *Bridge) run() {
	var altScreen bool
	var held []string

	for {
		select {
		case item := <-b.queue:
			if item.setAltScren {
				altScreen = item.altActive
				if !altScreen && len(held) > 0 {
					for _, text := range held {
						b.printNow(text)
					}
					held = nil
				}
				continue
			}
			if altScreen {
				// tea.Program.Println drops its output outright while
				// the alt screen is active ("If the altscreen is active
				// no output will be printed" — its own doc comment), so
				// anything committed while the Ctrl+R transcript view is
				// open is held here and flushed in order once it closes.
				held = append(held, item.text)
				// Also mirror it straight into the open transcript view
				// (see transcriptview.go's msgAltScreenAppend doc comment)
				// so Ctrl+R does not go stale for however long it stays
				// open.
				if p := b.prog(); p != nil {
					p.Send(msgAltScreenAppend{Text: item.text})
				}
				continue
			}
			b.printNow(item.text)
		case <-b.quit:
			return
		}
	}
}

func (b *Bridge) printNow(text string) {
	p := b.prog()
	if p != nil {
		p.Println(text)
	}
}

// SetProgram attaches the Bubbletea program. Must be called before the
// program starts receiving events that reach the bridge.
func (b *Bridge) SetProgram(p *tea.Program) {
	b.mu.Lock()
	b.program = p
	b.mu.Unlock()
}

// Stop shuts the bridge's committer goroutine down. Idempotent. Anything
// still queued when it fires is dropped rather than printed after the
// program has exited.
func (b *Bridge) Stop() {
	b.once.Do(func() { close(b.quit) })
}

func (b *Bridge) prog() *tea.Program {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.program
}

// Commit enqueues lines to be printed to the transcript, in order,
// relative to every other Commit/SetAltScreen call from any goroutine.
// Safe to call from Bubbletea's Update itself — unlike calling
// Program.Println directly, this never blocks waiting for the event loop,
// so it cannot deadlock it.
func (b *Bridge) Commit(lines []string) {
	if len(lines) == 0 {
		return
	}
	select {
	case b.queue <- bridgeItem{text: strings.Join(lines, "\n")}:
	case <-b.quit:
	}
}

// SetAltScreen tells the bridge whether the Ctrl+R transcript view is
// open. Enqueued like Commit, so a Commit and a SetAltScreen from the
// same Update call (e.g. closing the view) are applied in the order they
// were issued, not raced against each other.
func (b *Bridge) SetAltScreen(active bool) {
	select {
	case b.queue <- bridgeItem{setAltScren: true, altActive: active}:
	case <-b.quit:
	}
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
		name := titleCase(ev.ToolName)
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
		lines := append([]string{""}, RenderToolCall(view)...)
		b.Commit(lines)

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
			b.Commit([]string{fmt.Sprintf("%s %s %s %s %s%s", Dim(G().Call), Bold(e.Agent), Dim(e.Description), Dim("on"), e.ModelID, note)})
		case agent.SubagentEventDone:
			b.Commit([]string{Dim(fmt.Sprintf("  %s finished - %d tool calls, %d chars returned", e.Agent, e.ToolCalls, e.Chars))})
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
