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
	// lastTool is the most recent tool_start event's tool name, shown as
	// the row's "last action" until the row finishes.
	lastTool  string
	toolCalls int
	tokens    int
	message   string
}

// SubagentPanelState tracks live/done subagent rows for the turn in
// progress. Rows keep their arrival order and persist (done or errored)
// until Reset is called at the next turn's start — see app.go's
// beginTurn, the same turn-boundary signal the footer's busy flag uses.
type SubagentPanelState struct {
	rows  map[string]*subagentRow
	order []string
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
	p.rows = map[string]*subagentRow{}
	p.order = nil
}

// Apply folds one agent.SubagentEvent into the panel's row state.
func (p *SubagentPanelState) Apply(e agent.SubagentEvent) {
	if p == nil || e.ID == "" {
		return
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
		r.lastTool = e.ToolName
	case agent.SubagentEventDone:
		r.status = "done"
		r.toolCalls = e.ToolCalls
		r.tokens = e.Usage.TotalTokens
	case agent.SubagentEventError:
		r.status = "error"
		r.message = e.Message
	}
}

// Empty reports whether the panel has nothing to show (no dispatch has
// fired this turn).
func (p *SubagentPanelState) Empty() bool {
	return p == nil || len(p.order) == 0
}

// counts returns how many rows are still running vs. finished (done or
// errored), for the header's "N live · M done" meta.
func (p *SubagentPanelState) counts() (live, done int) {
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
// (docs/kiln-design-handoff/README.md "agents" row: "name (8 columns)").
const subagentNameWidth = 8

// subagentNameMax is where a long agent name is truncated with an
// ellipsis so the task column keeps its alignment.
const subagentNameMax = 16

// meterCells is the progress bar's width in cells.
const meterCells = 10

// tokensColWidth is the right-aligned tokens column's width
// (docs/kiln-design-handoff/README.md "agents" row: "tokens, right-aligned").
const tokensColWidth = 6

// Render draws the panel: a labelled hairline rule with "d/n done" meta,
// a header row ("N subagents running in parallel"/"N subagents
// finished"), then up to maxSubagentRows dispatches (two rows each:
// name+task, then the indented last-action row), then a "+K more" row when
// there are more. Returns nil when the panel is empty, so callers can
// append its result unconditionally.
func (p *SubagentPanelState) Render(width int) []string {
	if p.Empty() {
		return nil
	}
	live, done := p.counts()
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

// renderSubagentRow draws one dispatch's two rows: name (padded to
// subagentNameWidth, coloured by status) + task (Ink, truncated), a
// 10-cell progress bar and the token total (once done) right-aligned; then
// an indented row with the latest action ("→ " while running, "✓ " once
// done/errored), dim.
func renderSubagentRow(r *subagentRow, width int) []string {
	nameColor := KilnAmber
	actionGlyph := G().Action
	action := "starting…"
	switch r.status {
	case "done":
		nameColor = KilnGreen
		actionGlyph = G().OK
		action = "finished"
		if r.lastTool != "" {
			action = r.lastTool
		}
	case "error":
		nameColor = KilnRed
		actionGlyph = G().Fail
		action = r.message
	default:
		if r.lastTool != "" {
			action = r.lastTool
		}
	}
	name := r.agent
	if name == "" {
		name = "agent"
	}
	indent := ""
	if r.depth > 1 {
		indent = strings.Repeat("  ", r.depth-1)
	}
	namePadded := FitStatus(name, subagentNameMax)
	if pad := subagentNameWidth - VisibleWidth(namePadded); pad > 0 {
		namePadded += strings.Repeat(" ", pad)
	}
	left := fmt.Sprintf(" %s%s  %s", indent, nameColor(namePadded), Ink(r.description))

	// A finished dispatch reads as complete: the bar fills entirely
	// (design: "filled cells amber/green"); while running it tracks
	// tool calls.
	right := meterBar(r.toolCalls, nameColor)
	if r.status == "done" {
		right = meterBar(meterCells, nameColor)
	}
	tokens := ""
	if r.status == "done" {
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

	actionRow := FitStatus(strings.Repeat(" ", 10+len(indent))+Dim(actionGlyph+" "+action), width)
	return []string{nameRow, actionRow}
}

// meterBar draws a 10-cell progress meter: filled cells (coloured, capped
// at 10) track the dispatch's tool-call count as a simple, real activity
// signal — there is no task-completion percentage to show, so the meter
// reads as "how much work has this subagent done" rather than "how close
// is it to finishing". Empty cells use BarEmpty (#3f372c), the design's
// dedicated token for this one bar (theme.go).
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
	empty := strings.Repeat(G().MeterEmpty, cells-filled)
	return color(full) + BarEmpty(empty)
}
