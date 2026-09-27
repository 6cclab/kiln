package tui

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/msg"
)

// TestSubagentsPanel_TwoLiveRows checks that two concurrent task
// dispatches (internal/harness now runs several `task` calls from one
// assistant message in parallel) both show as running rows at once, with
// the header's "N live" count reflecting both.
func TestSubagentsPanel_TwoLiveRows(t *testing.T) {
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "general-purpose", Description: "find the bug"})
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc2", Agent: "general-purpose", Description: "write the fix"})

	rows := p.Render(100)
	joined := strings.Join(rows, "\n")

	if !strings.Contains(rows[1], "2 subagents running in parallel") {
		t.Fatalf("header missing running count: %q", rows[1])
	}
	if !strings.Contains(rows[0], "0/2 done") {
		t.Fatalf("label rule missing d/n done meta: %q", rows[0])
	}
	if !strings.Contains(joined, "find the bug") || !strings.Contains(joined, "write the fix") {
		t.Fatalf("both dispatches not shown:\n%s", joined)
	}
}

// TestSubagentsPanel_ToolEventUpdatesLastAction checks that a finished tool
// call (SubagentEventTool, reported on the subagent's own EventToolEnd —
// see dispatch.go) replaces its "starting…" placeholder with a formatted
// action line: the tool's title-cased name, its primary argument quoted,
// and a " · <first result line>" summary (design: '→ Grep "app.use(" · 14
// matches'). *qa/findings/20260927T000638Z-subagent-row-no-action.json*.
func TestSubagentsPanel_ToolEventUpdatesLastAction(t *testing.T) {
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "general-purpose", Description: "find the bug"})

	before := strings.Join(p.Render(100), "\n")
	if !strings.Contains(before, "starting") {
		t.Fatalf("row should start with the placeholder action:\n%s", before)
	}

	p.Apply(agent.SubagentEvent{
		Kind:     agent.SubagentEventTool,
		ID:       "tc1",
		ToolName: "grep",
		ToolArgs: map[string]any{"pattern": "app.use("},
		ToolResult: &msg.ToolResultMessage{
			Content: msg.Blocks{msg.Text("14 matches\nsrc/app.js:12")},
		},
	})
	after := strings.Join(p.Render(100), "\n")
	if !strings.Contains(after, `Grep "app.use(" · 14 matches`) {
		t.Fatalf("row did not pick up the formatted action line:\n%s", after)
	}
	if strings.Contains(after, "starting") {
		t.Fatalf("placeholder action should be gone once a tool ran:\n%s", after)
	}
}

// TestSubagentsPanel_ToolEventWithNoResultShowsNameAndArg checks a tool
// call whose result carries no summarizable text still shows the tool name
// and argument (no dangling " · ").
func TestSubagentsPanel_ToolEventWithNoResultShowsNameAndArg(t *testing.T) {
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "general-purpose", Description: "find the bug"})
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventTool, ID: "tc1", ToolName: "bash_background", ToolArgs: map[string]any{"command": "sleep 30"}})
	after := strings.Join(p.Render(100), "\n")
	if !strings.Contains(after, `Bash background "sleep 30"`) {
		t.Fatalf("row missing name+arg action line:\n%s", after)
	}
	if strings.Contains(after, " · ") {
		t.Fatalf("row should not show a dangling summary separator:\n%s", after)
	}
}

// TestSubagentsPanel_DoneRowShowsFinalResultLine checks the design's "done"
// action line: the subagent's final answer's first line, not the last tool
// it happened to call. *qa/findings/20260927T000638Z-subagent-row-no-
// action.json*.
func TestSubagentsPanel_DoneRowShowsFinalResultLine(t *testing.T) {
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "general-purpose", Description: "find the bug"})
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventTool, ID: "tc1", ToolName: "read", ToolArgs: map[string]any{"file_path": "src/routes/upload.ts"}})
	p.Apply(agent.SubagentEvent{
		Kind: agent.SubagentEventDone,
		ID:   "tc1",
		Text: "Upload route mounts at src/routes/upload.ts:22\n\nMore detail the row has no room for.",
	})
	after := strings.Join(p.Render(100), "\n")
	if !strings.Contains(after, "✓ Upload route mounts at src/routes/upload.ts:22") {
		t.Fatalf("done row did not show the final result's first line:\n%s", after)
	}
	if strings.Contains(after, "More detail") {
		t.Fatalf("done row should only show the first line:\n%s", after)
	}
}

// TestSubagentsPanel_DoneRowShowsTokens checks that a done event moves the
// row out of the live count and prints its token total.
func TestSubagentsPanel_DoneRowShowsTokens(t *testing.T) {
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "general-purpose", Description: "find the bug"})
	p.Apply(agent.SubagentEvent{
		Kind:      agent.SubagentEventDone,
		ID:        "tc1",
		ToolCalls: 3,
		Usage:     msg.Usage{TotalTokens: 1234},
	})

	rows := p.Render(100)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(rows[1], "1 subagents finished") {
		t.Fatalf("header did not move the row to done: %q", rows[1])
	}
	if !strings.Contains(rows[0], "1/1 done") {
		t.Fatalf("label rule missing d/n done meta: %q", rows[0])
	}
	if !strings.Contains(joined, "1.2k") {
		t.Fatalf("done row missing its token total:\n%s", joined)
	}
}

// TestSubagentsPanel_ErrorRowShowsMessage checks an error event marks the
// row done (out of the live count) and shows its failure message.
func TestSubagentsPanel_ErrorRowShowsMessage(t *testing.T) {
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "general-purpose", Description: "find the bug"})
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventError, ID: "tc1", Message: "boom"})

	rows := p.Render(100)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(rows[1], "1 subagents finished") {
		t.Fatalf("header did not move the errored row out of live: %q", rows[1])
	}
	if !strings.Contains(rows[0], "1/1 done") {
		t.Fatalf("label rule missing d/n done meta: %q", rows[0])
	}
	if !strings.Contains(joined, "boom") {
		t.Fatalf("error row missing its message:\n%s", joined)
	}
}

// TestSubagentsPanel_CapsAtSixRows checks the "+K more" collapse once more
// than maxSubagentRows dispatches have fired in one turn.
func TestSubagentsPanel_CapsAtSixRows(t *testing.T) {
	p := NewSubagentPanelState()
	for i := 0; i < 8; i++ {
		p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: string(rune('a' + i)), Agent: "general-purpose", Description: "task"})
	}
	rows := p.Render(100)
	// label rule + header + 2 rows per dispatch (name/task, then the
	// indented last-action row) + the "+K more" row.
	want := 2 + maxSubagentRows*2 + 1
	if len(rows) != want {
		t.Fatalf("got %d rows, want %d (rule + header + %d rows*2 + more row)", len(rows), want, maxSubagentRows)
	}
	if !strings.Contains(rows[len(rows)-1], "+2 more") {
		t.Fatalf("last row should read '+2 more': %q", rows[len(rows)-1])
	}
}

// TestSubagentsPanel_EmptyRendersNothing checks the panel draws nothing
// (not even a header) when no dispatch has fired this turn, so it costs no
// rows in the common case.
func TestSubagentsPanel_EmptyRendersNothing(t *testing.T) {
	p := NewSubagentPanelState()
	if rows := p.Render(100); rows != nil {
		t.Fatalf("empty panel should render nothing, got %v", rows)
	}
}

// TestModel_SubagentsPanel_VisibleInLiveLines checks the panel appears in
// the Model's live region once two dispatches start, and is absent before
// any have. Exercises the same liveLines path both inline and fullscreen
// View use.
func TestModel_SubagentsPanel_VisibleInLiveLines(t *testing.T) {
	m := newTestModel()
	if got := strings.Join(viewLines(m), "\n"); strings.Contains(got, "subagents") {
		t.Fatalf("subagents panel should not show before any dispatch:\n%s", got)
	}

	m.subagents.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "general-purpose", Description: "find the bug"})
	m.subagents.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc2", Agent: "general-purpose", Description: "write the fix"})

	got := strings.Join(viewLines(m), "\n")
	if !strings.Contains(got, "subagents") || !strings.Contains(got, "2 subagents running in parallel") {
		t.Fatalf("subagents panel should show two live rows:\n%s", got)
	}
}

// TestModel_SubagentsPanel_ClearedOnNextTurn checks beginTurn — the
// footer's own turn-boundary signal (SetBusy(true)) — also resets the
// subagents panel, so a finished turn's rows do not leak into the next
// one.
func TestModel_SubagentsPanel_ClearedOnNextTurn(t *testing.T) {
	m := newTestModel()
	m.subagents.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "general-purpose", Description: "find the bug"})
	m.subagents.Apply(agent.SubagentEvent{Kind: agent.SubagentEventDone, ID: "tc1", Usage: msg.Usage{TotalTokens: 10}})
	if m.subagents.Empty() {
		t.Fatalf("setup: panel should not be empty before the next turn")
	}

	next, _ := m.beginTurn("do another thing", nil)
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("beginTurn did not return a Model")
	}
	if !nm.subagents.Empty() {
		t.Fatalf("subagents panel should be cleared at the start of the next turn")
	}
}

// TestSubagentsPanel_LongNameHeldToColumnWidth checks that a name longer
// than subagentNameWidth (8) is truncated to fit the column rather than
// overflowing it, so the task column starts at the same place regardless
// of the agent name's length (Terminal.dc.html's 4-column grid:
// `8ch minmax(0,1fr) 11ch 6ch`).
func TestSubagentsPanel_LongNameHeldToColumnWidth(t *testing.T) {
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "short", Agent: "scout", Description: "short name task"})
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "long", Agent: "general-purpose", Description: "long name task"})

	rows := p.Render(100)
	var shortRow, longRow string
	for _, r := range rows {
		if strings.Contains(r, "short name task") {
			shortRow = r
		}
		if strings.Contains(r, "long name task") {
			longRow = r
		}
	}
	if shortRow == "" || longRow == "" {
		t.Fatalf("rows missing:\n%s", strings.Join(rows, "\n"))
	}
	// Compare by visible (rune) width, not byte offset: the truncated long
	// name ends in "…", a multi-byte rune, so a byte-offset comparison
	// would report a false mismatch even when the two rows line up.
	shortTaskCol := len([]rune(shortRow[:strings.Index(shortRow, "short name task")]))
	longTaskCol := len([]rune(longRow[:strings.Index(longRow, "long name task")]))
	if shortTaskCol != longTaskCol {
		t.Fatalf("task column shifted by a long name: short at %d, long at %d\nshort: %q\nlong:  %q", shortTaskCol, longTaskCol, shortRow, longRow)
	}
}

// TestSubagentsPanel_RunningRowShowsTokensOnceKnown checks that a running
// row shows its token total once it is greater than zero, rather than
// waiting for the row to reach "done" (Terminal.dc.html line 187-189: a
// running scout carries `tok:'6.3k'`).
func TestSubagentsPanel_RunningRowShowsTokensOnceKnown(t *testing.T) {
	r := &subagentRow{status: "running", tokens: 6300, description: "survey"}
	lines := renderSubagentRow(r, 100)
	if !strings.Contains(lines[0], "6.3k") {
		t.Fatalf("running row with a nonzero token count should show it:\n%s", lines[0])
	}

	zero := &subagentRow{status: "running", tokens: 0, description: "survey"}
	zeroLines := renderSubagentRow(zero, 100)
	if strings.Contains(zeroLines[0], "0") {
		t.Fatalf("running row with no known token count should not show one:\n%s", zeroLines[0])
	}
}

// TestSubagentsPanel_NestedRowIsIndented checks that a dispatch made from
// inside a subagent (depth 2) renders indented under the depth-1 rows, so
// an orchestrator's own children read as its children.
func TestSubagentsPanel_NestedRowIsIndented(t *testing.T) {
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "heavy-orchestrator", Description: "plan the work", Depth: 1})
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc2", Agent: "general-purpose", Description: "write a script", Depth: 2})

	rows := p.Render(100)
	var top, nested string
	for _, r := range rows {
		if strings.Contains(r, "plan the work") {
			top = r
		}
		if strings.Contains(r, "write a script") {
			nested = r
		}
	}
	if top == "" || nested == "" {
		t.Fatalf("rows missing:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(nested, "   ") || strings.HasPrefix(top, "   ") {
		t.Fatalf("depth-2 row should be indented past the depth-1 row:\n%q\n%q", top, nested)
	}
}

// TestSubagentPanel_UsageEventDoesNotUnfreeze guards the interaction
// between the running-token figure and live_freeze.go's "live while
// last" rule: a usage event must update the row's tokens without making
// an already-frozen (already-committed) panel live again, or every model
// turn of every running subagent would resurrect it and the next commit
// would write a near-identical copy into the transcript.
func TestSubagentPanel_UsageEventDoesNotUnfreeze(t *testing.T) {
	p := &SubagentPanelState{}
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "1", Agent: "scout", Description: "look around", Depth: 1})

	// FreezeFinal, not Freeze: a still-running panel deliberately stays
	// live (see TestSubagentsPanel_RunningPanelCommitsOnlyOnce).
	if lines := p.FreezeFinal(80); len(lines) == 0 {
		t.Fatal("FreezeFinal returned no lines for a live panel")
	}
	if got := p.Render(80); got != nil {
		t.Fatalf("panel still live after Freeze: %q", got)
	}

	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventUsage, ID: "1", Usage: msg.Usage{TotalTokens: 1234}})
	if got := p.Render(80); got != nil {
		t.Errorf("a usage event unfroze the panel; Render = %q", got)
	}
	if lines := p.FreezeFinal(80); lines != nil {
		t.Errorf("a usage event made the panel committable again; FreezeFinal = %q", lines)
	}

	// A material event still resurrects the panel, and the token total
	// recorded while frozen is visible once it does.
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventTool, ID: "1", ToolName: "bash"})
	live := p.Render(80)
	if live == nil {
		t.Fatal("a tool event did not make the panel live again")
	}
	if !strings.Contains(strings.Join(live, "\n"), "1.2k") {
		t.Errorf("token total recorded while frozen is missing once live again:\n%s", strings.Join(live, "\n"))
	}
}

// TestSubagentsPanel_PlainModeKeepsFullName pins the accessibility
// carve-out: the 8-column name is a visual grid constraint, but the
// subagents panel is the only transcript record of a dispatch, so under
// --ax-screen-reader the agent's full name must survive.
func TestSubagentsPanel_PlainModeKeepsFullName(t *testing.T) {
	SetPlainMode(true)
	defer SetPlainMode(false)

	p := &SubagentPanelState{}
	p.Apply(agent.SubagentEvent{
		Kind: agent.SubagentEventStart, ID: "1",
		Agent: "general-purpose", Description: "look something up", Depth: 1,
	})
	out := strings.Join(p.Render(100), "\n")
	if !strings.Contains(out, "general-purpose") {
		t.Errorf("plain mode truncated the agent name:\n%s", out)
	}
	if !strings.Contains(out, "look something up") {
		t.Errorf("plain mode lost the dispatch description:\n%s", out)
	}
}

// TestSubagentsPanel_RunningPanelCommitsOnlyOnce pins the one-block rule:
// the design has a single agents block that updates in place, so a
// dispatch that outlives other commits must not write a copy of itself
// into the transcript each time something else commits. A running panel
// stays live; only FreezeFinal (finishTurn) commits it.
func TestSubagentsPanel_RunningPanelCommitsOnlyOnce(t *testing.T) {
	p := &SubagentPanelState{}
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "1", Agent: "scout", Description: "look", Depth: 1})

	// Three unrelated commits happen while the dispatch runs.
	for i := 0; i < 3; i++ {
		if lines := p.Freeze(80); lines != nil {
			t.Fatalf("commit %d froze a still-running panel: %q", i, lines)
		}
		if p.Render(80) == nil {
			t.Fatalf("panel stopped rendering live after commit %d", i)
		}
		p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventTool, ID: "1", ToolName: "bash"})
	}

	// Once the dispatch is done the ordinary freeze path commits it.
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventDone, ID: "1", ToolCalls: 3, Usage: msg.Usage{TotalTokens: 900}})
	first := p.Freeze(80)
	if len(first) == 0 {
		t.Fatal("a finished panel did not commit")
	}
	if again := p.Freeze(80); again != nil {
		t.Errorf("finished panel committed twice: %q", again)
	}
	if final := p.FreezeFinal(80); final != nil {
		t.Errorf("FreezeFinal re-committed an already-committed panel: %q", final)
	}
}

// TestSubagentsPanel_FreezeFinalCommitsStillRunningPanel covers the
// abort case: finishTurn must land the panel in the transcript even when
// a dispatch never reported a terminal state.
func TestSubagentsPanel_FreezeFinalCommitsStillRunningPanel(t *testing.T) {
	p := &SubagentPanelState{}
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "1", Agent: "scout", Description: "look", Depth: 1})

	if lines := p.Freeze(80); lines != nil {
		t.Fatalf("ordinary freeze committed a running panel: %q", lines)
	}
	if lines := p.FreezeFinal(80); len(lines) == 0 {
		t.Fatal("FreezeFinal did not commit a still-running panel")
	}
}
