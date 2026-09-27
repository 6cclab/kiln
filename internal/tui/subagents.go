package tui

// The subagents panel: a live-region block shown above the input while at
// least one `task` dispatch has fired in the current turn
// (docs/kiln-design.md's "agents / subagents" row: header + rows of name,
// task, last action, progress, tokens). Concurrent task calls in one
// assistant message run in parallel (internal/harness), so several rows
// can be live at once; this file tracks that per-turn state and renders
// it, following the same labelRule + row pattern every other transcript
// block in theme.go/transcript.go uses.

import (
	"fmt"
	"strings"
	"sync"

	"github.com/andrepato/harness/internal/agent"
)

// maxSubagentRows caps how many dispatch rows the panel draws before
// collapsing the rest into a "+K more" row, keeping the live region's
// height bounded regardless of how many tasks one turn dispatches.
const maxSubagentRows = 6

// subagentRow is one dispatch's accumulated state, keyed by the task tool
// call's id (agent.SubagentEvent.ID) so start/tool/done/error events for
// the same dispatch update the same row instead of appending new ones.
type subagentRow struct {
	agent       string
	description string
	providerID  string
	modelID     string
	// depth is the dispatch tree depth (1 for a subagent of the main
	// session); nested rows are indented by it so a heavy-role
	// orchestrator's own dispatches read as its children.
	depth int
	// status is "running", "done" or "error". A row starts "running" on
	// its start event and never reverts once it reaches "done"/"error".
	status string
	// lastAction is the most recent finished tool call's formatted action
	// line ("Grep \"app.use(\" · 14 matches", formatSubagentAction), shown
	// under the row while it is still running. Empty until the first tool
	// call finishes, in which case the row shows the "starting…"
	// placeholder instead.
	lastAction string
	// resultText is the subagent's final answer (SubagentEventDone's
	// Text), shown as the done row's action line (its first line, design:
	// "✓ Upload route mounts at src/routes/upload.ts:22") in place of
	// "finished".
	resultText string
	toolCalls  int
	tokens     int
	message    string
	// displayName is the row's rendered name, computed per-render by
	// assignDisplayNamesLocked from agent (the raw dispatch type) and its
	// siblings in the panel — see that function's doc comment. Empty until
	// a render has happened; renderSubagentRow falls back to agent (then
	// "agent") when it is unset, which is also what a subagentRow built
	// directly (bypassing the panel, as some tests do) gets.
	displayName string
}

// SubagentPanelState tracks live/done subagent rows for the turn in
// progress. Rows keep their arrival order and persist (done or errored)
// until Reset is called at the next turn's start — see app.go's
// beginTurn, the same turn-boundary signal the footer's busy flag uses.
type SubagentPanelState struct {
	// mu guards every field below: Apply/Reset run on the Update goroutine
	// (MsgSubagentEvent's handler, beginTurn), but Render/Freeze/Empty are
	// also called from Bridge's freeze hook (live_freeze.go), which can run
	// on any goroutine that calls a commit method — the harness event bus,
	// a subagent dispatcher, or Update itself. See live_freeze.go's doc
	// comment for the full freeze mechanism this guards.
	mu    sync.Mutex
	rows  map[string]*subagentRow
	order []string
	// frozen is true once the panel's current state has been committed to
	// the transcript as a synthetic block (see Freeze) and it has dropped
	// out of the live region; Apply clears it, so a new subagent event
	// makes the panel live again with its (now updated) full state.
	frozen bool
}

// NewSubagentPanelState returns an empty panel.
func NewSubagentPanelState() *SubagentPanelState {
	return &SubagentPanelState{rows: map[string]*subagentRow{}}
}

// Reset clears every row, called at the start of each new turn so a
// previous turn's dispatches do not linger into the next one's panel.
func (p *SubagentPanelState) Reset() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rows = map[string]*subagentRow{}
	p.order = nil
	p.frozen = false
}

// Apply folds one agent.SubagentEvent into the panel's row state, and
// makes the panel live again (frozen = false) — see the frozen field's
// doc comment.
//
// A usage event is the one exception: it carries only a token total, and
// unfreezing on it would resurrect an already-committed panel on every
// model turn of every running subagent, so the next unrelated commit
// would freeze and commit a near-identical copy (see live_freeze.go's
// "live while last" rule). Tokens still land in the row state here, so a
// panel that is *already* live picks them up on its next frame.
func (p *SubagentPanelState) Apply(e agent.SubagentEvent) {
	if p == nil || e.ID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.Kind != agent.SubagentEventUsage {
		p.frozen = false
	}
	if p.rows == nil {
		p.rows = map[string]*subagentRow{}
	}
	r, ok := p.rows[e.ID]
	if !ok {
		r = &subagentRow{status: "running"}
		p.rows[e.ID] = r
		p.order = append(p.order, e.ID)
	}
	if e.Agent != "" {
		r.agent = e.Agent
	}
	switch e.Kind {
	case agent.SubagentEventStart:
		r.description = e.Description
		r.providerID = e.ProviderID
		r.modelID = e.ModelID
		r.depth = e.Depth
		r.status = "running"
	case agent.SubagentEventTool:
		r.toolCalls++
		r.lastAction = formatSubagentAction(e)
	case agent.SubagentEventUsage:
		// Running total for a row that has not finished yet; Done
		// overwrites it with the final figure.
		r.tokens = e.Usage.TotalTokens
	case agent.SubagentEventDone:
		r.status = "done"
		r.toolCalls = e.ToolCalls
		r.tokens = e.Usage.TotalTokens
		r.resultText = firstNonEmptyLine(e.Text)
	case agent.SubagentEventError:
		r.status = "error"
		r.message = e.Message
	}
}

// Empty reports whether the panel has nothing to show (no dispatch has
// fired this turn).
func (p *SubagentPanelState) Empty() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emptyLocked()
}

func (p *SubagentPanelState) emptyLocked() bool {
	return len(p.order) == 0
}

// counts returns how many rows are still running vs. finished (done or
// errored), for the header's "N live · M done" meta. Caller must hold mu.
func (p *SubagentPanelState) countsLocked() (live, done int) {
	for _, id := range p.order {
		if p.rows[id].status == "running" {
			live++
		} else {
			done++
		}
	}
	return live, done
}

// subagentNameWidth is the name column's fixed width
// (docs/kiln-design-handoff/Terminal.dc.html lines 91-102: the agents row
// is a 4-column grid, `8ch minmax(0,1fr) 11ch 6ch`, so the name column is
// held to 8 columns exactly). Names longer than this are truncated to fit
// so the task column always starts at the same place; there is no separate
// "max before ellipsis" width (see subagentNameMax's old doc comment) —
// that only let long names overflow the grid and shift the task column.
const subagentNameWidth = 8

// meterCells is the progress bar's width in cells.
const meterCells = 10

// tokensColWidth is the right-aligned tokens column's width
// (docs/kiln-design-handoff/README.md "agents" row: "tokens, right-aligned").
const tokensColWidth = 6

// Render draws the panel for the live region: a labelled hairline rule
// with "d/n done" meta, a header row ("N subagents running in
// parallel"/"N subagents finished"), then up to maxSubagentRows dispatches
// (two rows each: name+task, then the indented last-action row), then a
// "+K more" row when there are more. Returns nil when the panel is empty
// or frozen (see the frozen field's doc comment — a frozen panel has
// already committed its current state and drops out of the live region
// until Apply makes it live again), so callers can append its result
// unconditionally.
func (p *SubagentPanelState) Render(width int) []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.frozen || p.emptyLocked() {
		return nil
	}
	return p.renderLocked(width)
}

// Freeze marks the panel frozen and returns its current state as
// commit-ready lines (a leading blank row, matching every other commit
// call site in app.go), or nil if the panel is already frozen or empty —
// see live_freeze.go.
//
// While any dispatch is still running this returns nil and leaves the
// panel live. The design has exactly one agents block that "updates in
// place" (docs/kiln-design-handoff/Terminal.dc.html line 91-102), but the
// generic "live while last" rule would freeze and commit a snapshot every
// time anything else commits, and the next subagent event makes the panel
// live again — so a dispatch that outlives a few tool blocks or notes
// wrote a near-identical copy of itself into the transcript each time. A
// running panel therefore stays in the live region, where it is still
// fully visible and updating, and commits exactly once: through
// FreezeFinal, when the dispatch is over.
func (p *SubagentPanelState) Freeze(width int) []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if live, _ := p.countsLocked(); live > 0 {
		return nil
	}
	return p.freezeLocked(width)
}

// FreezeFinal is Freeze without the still-running check: the panel's one
// commit, called from finishTurn once the turn (and so every dispatch in
// it, however it ended) is over. Idempotent with Freeze — whichever runs
// first commits, the other returns nil.
func (p *SubagentPanelState) FreezeFinal(width int) []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.freezeLocked(width)
}

// freezeLocked is Freeze's body, callable with mu already held.
func (p *SubagentPanelState) freezeLocked(width int) []string {
	if p.frozen || p.emptyLocked() {
		return nil
	}
	p.frozen = true
	return append([]string{""}, p.renderLocked(width)...)
}

// renderLocked is Render's body, callable with mu already held (by Render
// or Freeze).
func (p *SubagentPanelState) renderLocked(width int) []string {
	p.assignDisplayNamesLocked()
	live, done := p.countsLocked()
	total := live + done
	header := fmt.Sprintf("%d subagents running in parallel", total)
	if live == 0 {
		header = fmt.Sprintf("%d subagents finished", total)
	}
	lines := []string{
		labelRule("subagents", Muted, fmt.Sprintf("%d/%d done", done, total), width),
		Muted(header),
	}

	shown := p.order
	more := 0
	if len(shown) > maxSubagentRows {
		more = len(shown) - maxSubagentRows
		shown = shown[:maxSubagentRows]
	}
	for _, id := range shown {
		lines = append(lines, renderSubagentRow(p.rows[id], width)...)
	}
	if more > 0 {
		lines = append(lines, FitStatus("  "+Muted(fmt.Sprintf("+%d more", more)), width))
	}
	return lines
}

// assignDisplayNamesLocked gives every row in the panel a distinct name
// that fits the 8-column name slot, fixing *qa/findings/…-subagent-names-
// indistinct.json*: three concurrent `general-purpose` dispatches used to
// all render as "general…" (the type name truncated to the column width),
// indistinguishable except by task text.
//
// The rule: a dispatch type that appears once in the panel keeps its
// plain short name — the type's first word (shortAgentWord), e.g. "scout"
// stays "scout" and "general-purpose" becomes "general" (both fit the
// column with no ellipsis truncation, unlike the raw type name). A type
// that appears more than once gets a 1-based "-N" index in arrival order
// (indexedAgentName): the word itself when it fits the slot with the
// index ("scout-1", as in the design's agents scene), else its first three
// letters ("gen-1" for general-purpose) — a clean abbreviation, not a
// word cut short.
//
// Plain mode (--ax-screen-reader) has no column to hold, so it keeps the
// existing full-name behaviour: a singleton keeps its full type name
// unchanged, and duplicates get the full type name plus "-N" rather than
// a 6-character truncation, since there is no room constraint pushing the
// screen reader's transcript record to be shorter.
func (p *SubagentPanelState) assignDisplayNamesLocked() {
	counts := map[string]int{}
	for _, id := range p.order {
		counts[p.rows[id].agent]++
	}
	seen := map[string]int{}
	for _, id := range p.order {
		r := p.rows[id]
		typ := r.agent
		if typ == "" {
			r.displayName = ""
			continue
		}
		if counts[typ] <= 1 {
			if IsPlain() {
				r.displayName = typ
			} else {
				r.displayName = shortAgentWord(typ)
			}
			continue
		}
		seen[typ]++
		idx := seen[typ]
		if IsPlain() {
			r.displayName = fmt.Sprintf("%s-%d", typ, idx)
		} else {
			r.displayName = indexedAgentName(shortAgentWord(typ), idx)
		}
	}
}

// indexedAgentName is word plus "-idx", kept within the 8-column name slot:
// the whole word when it fits, else its first three letters.
func indexedAgentName(word string, idx int) string {
	suffix := fmt.Sprintf("-%d", idx)
	if len(word)+len(suffix) > 8 {
		word = truncateWord(word, 3)
	}
	return word + suffix
}

// shortAgentWord returns typ's first word: the run of letters/digits/'_'
// up to the first separator ('-', ' ', etc.), or typ unchanged if it has
// none — "general-purpose" -> "general", "code-reviewer" -> "code",
// "scout" -> "scout".
func shortAgentWord(typ string) string {
	for i, c := range typ {
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return typ[:i]
	}
	return typ
}

// truncateWord cuts s to at most n bytes (agent type identifiers are
// plain ASCII, so byte and rune counts agree).
func truncateWord(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// renderSubagentRow draws one dispatch's two rows: name (padded to
// subagentNameWidth, coloured by status) + task (Ink, truncated), a
// 10-cell progress bar and the token total (once known, i.e. greater than
// zero — see r.tokens) right-aligned; then an indented row with the latest
// action ("→ " while running, "✓ " once done/errored), dim.
func renderSubagentRow(r *subagentRow, width int) []string {
	nameColor := KilnAmber
	actionGlyph := G().Action
	action := "starting…"
	switch r.status {
	case "done":
		nameColor = KilnGreen
		actionGlyph = G().OK
		action = "finished"
		if r.resultText != "" {
			action = r.resultText
		}
	case "error":
		nameColor = KilnRed
		actionGlyph = G().Fail
		action = r.message
	default:
		if r.lastAction != "" {
			action = r.lastAction
		}
	}
	name := r.displayName
	if name == "" {
		name = r.agent
	}
	if name == "" {
		name = "agent"
	}
	indent := ""
	if r.depth > 1 {
		indent = strings.Repeat("  ", r.depth-1)
	}
	// The 8-column name is a visual constraint from the design's grid
	// (docs/kiln-design-handoff/Terminal.dc.html line 95,
	// "grid-template-columns: 8ch …"). A screen reader has no columns to
	// keep, and truncating there would drop the agent's identity from the
	// only transcript record of a dispatch, so plain mode keeps the name
	// whole and the row's own columns widen to match.
	nameWidth := subagentNameWidth
	namePadded := name
	if IsPlain() {
		if w := VisibleWidth(name); w > nameWidth {
			nameWidth = w
		}
	} else {
		namePadded = FitStatus(name, subagentNameWidth)
	}
	if pad := nameWidth - VisibleWidth(namePadded); pad > 0 {
		namePadded += strings.Repeat(" ", pad)
	}
	// taskCol is where the task text (and so the action row's glyph below
	// it) starts: 1 (leading space) + indent + the name column + the
	// 2-space gap before the task — "name column + 2", not a fixed 10
	// columns, so a nested (depth > 1) row's action glyph still lines up
	// under its own task text.
	taskCol := 1 + len(indent) + nameWidth + 2
	left := fmt.Sprintf(" %s%s  %s", indent, nameColor(namePadded), Ink(r.description))

	// A finished dispatch reads as complete: the bar fills entirely
	// (design: "filled cells amber/green"); while running it tracks
	// tool calls.
	right := meterBar(r.toolCalls, nameColor)
	if r.status == "done" {
		right = meterBar(meterCells, nameColor)
	}
	tokens := ""
	if r.status == "done" || r.tokens > 0 {
		tokens = FormatTokens(r.tokens)
	}
	styledTokens := Muted(tokens)
	if pad := tokensColWidth - VisibleWidth(tokens); pad > 0 {
		styledTokens = strings.Repeat(" ", pad) + styledTokens
	}
	right += "  " + styledTokens

	pad := width - VisibleWidth(left) - VisibleWidth(right)
	nameRow := left
	if pad >= 1 {
		nameRow = left + strings.Repeat(" ", pad) + right
	} else {
		nameRow = FitStatus(left, width)
	}

	actionRow := FitStatus(strings.Repeat(" ", taskCol)+Dim(actionGlyph+" "+action), width)
	return []string{nameRow, actionRow}
}

// formatSubagentAction builds a subagent row's live "last action" line from
// a finished tool call (SubagentEventTool, now reported on EventToolEnd —
// see dispatch.go): "<ToolName> \"<primary arg>\" · <first result line>",
// each part included only when there is something to show, matching the
// design's own example ('→ Grep "app.use(" · 14 matches'). Reuses the same
// PrimaryArg/summarizeToolResult helpers the parent transcript's own tool
// blocks use (bridge.go), so a subagent's action line reads the same way a
// top-level tool call's collapsed summary does.
func formatSubagentAction(e agent.SubagentEvent) string {
	line := MapToolName(e.ToolName)
	if arg := PrimaryArg(e.ToolArgs); arg != "" {
		line += fmt.Sprintf(" %q", arg)
	}
	if summary := summarizeToolResult(e.ToolResult); len(summary) > 0 {
		if first := firstNonEmptyLine(strings.Join(summary, "\n")); first != "" {
			line += " · " + first
		}
	}
	return line
}

// firstNonEmptyLine returns s's first non-blank line, trimmed — used for a
// subagent's final answer (SubagentEventDone.Text) and a finished tool
// call's result summary, neither of which the row has room to show in
// full. permission_render.go's own firstLine (a plan's first line for the
// approval prompt) does not skip a leading blank line, which the plan text
// it reads never has; this one does, since a subagent's streamed text can.
func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

// meterBar draws a 10-cell progress meter: filled cells (coloured, capped
// at 10) track the dispatch's tool-call count as a simple, real activity
// signal — there is no task-completion percentage to show, so the meter
// reads as "how much work has this subagent done" rather than "how close
// is it to finishing". Empty cells use the same heavy glyph as filled
// cells (Terminal.dc.html line 378: `on:'━'.repeat(k), off:'━'.repeat(10-k)`
// — both runs are '━', distinguished only by colour), coloured with
// BarEmpty (#3f372c), the design's dedicated token for this one bar
// (theme.go). G().MeterEmpty ('─' in the real-terminal glyph set) is not
// used here — it reads as a different, thinner glyph than the filled
// cells, which is the bug this fixes.
func meterBar(toolCalls int, color func(string) string) string {
	const cells = meterCells
	filled := toolCalls
	if filled > cells {
		filled = cells
	}
	if filled < 0 {
		filled = 0
	}
	full := strings.Repeat(G().MeterFull, filled)
	empty := strings.Repeat(G().MeterFull, cells-filled)
	return color(full) + BarEmpty(empty)
}
