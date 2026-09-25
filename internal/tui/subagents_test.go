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

	if !strings.Contains(joined, "2 live") {
		t.Fatalf("header missing live count: %q", rows[0])
	}
	if !strings.Contains(joined, "find the bug") || !strings.Contains(joined, "write the fix") {
		t.Fatalf("both dispatches not shown:\n%s", joined)
	}
}

// TestSubagentsPanel_ToolEventUpdatesLastAction checks that a tool_start
// event for a running dispatch replaces its "starting…" placeholder with
// the tool name, prefixed by the action glyph.
func TestSubagentsPanel_ToolEventUpdatesLastAction(t *testing.T) {
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "tc1", Agent: "general-purpose", Description: "find the bug"})

	before := strings.Join(p.Render(100), "\n")
	if !strings.Contains(before, "starting") {
		t.Fatalf("row should start with the placeholder action:\n%s", before)
	}

	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventTool, ID: "tc1", ToolName: "grep"})
	after := strings.Join(p.Render(100), "\n")
	if !strings.Contains(after, "grep") {
		t.Fatalf("row did not pick up the tool name:\n%s", after)
	}
	if strings.Contains(after, "starting") {
		t.Fatalf("placeholder action should be gone once a tool ran:\n%s", after)
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
	if !strings.Contains(rows[0], "0 live") || !strings.Contains(rows[0], "1 done") {
		t.Fatalf("header did not move the row to done: %q", rows[0])
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
	if !strings.Contains(rows[0], "0 live") || !strings.Contains(rows[0], "1 done") {
		t.Fatalf("header did not move the errored row out of live: %q", rows[0])
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
	// header + maxSubagentRows rows + the "+K more" row.
	if len(rows) != 1+maxSubagentRows+1 {
		t.Fatalf("got %d rows, want %d (header + %d rows + more row)", len(rows), 1+maxSubagentRows+1, maxSubagentRows)
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
	if !strings.Contains(got, "subagents") || !strings.Contains(got, "2 live") {
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
