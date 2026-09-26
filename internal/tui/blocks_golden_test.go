package tui

import (
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/msg"
)

// Render snapshots for Phase 2's transcript blocks (tool meta, diff, plan,
// subagents, note, context) — see internal/tui/golden_test.go for the
// harness these use.

func TestRenderGolden_ToolOKMeta(t *testing.T) {
	withRenderEnv(t, 80)
	view := ToolCallView{
		Name:        "Bash",
		PrimaryArg:  "npm test -- upload",
		Status:      CallOK,
		Meta:        "approved · 4.1s",
		ResultLines: []string{"12 passed"},
	}
	assertRenderGolden(t, "tool-ok-meta", RenderToolCall(view))
}

func TestRenderGolden_ToolError(t *testing.T) {
	withRenderEnv(t, 80)
	view := ToolCallView{
		Name:        "Bash",
		PrimaryArg:  "npm test -- upload",
		Status:      CallError,
		Meta:        "auto-approved · 0.8s",
		ResultLines: []string{"1 failed", "Error: expected 200, got 500"},
	}
	assertRenderGolden(t, "tool-error", RenderToolCall(view))
}

func TestRenderGolden_DiffModified(t *testing.T) {
	withRenderEnv(t, 80)
	view := ToolCallView{
		Name:       "Update",
		PrimaryArg: "src/math.js",
		Status:     CallOK,
		Diff: &ToolDiff{
			Added:   1,
			Removed: 1,
			Lines: []DiffLine{
				{Num: 1, Sign: ' ', Text: "function add(a,b){"},
				{Num: 2, Sign: '-', Text: "  return a - b"},
				{Num: 2, Sign: '+', Text: "  return a + b"},
				{Num: 3, Sign: ' ', Text: "}"},
			},
		},
	}
	assertRenderGolden(t, "diff-modified", RenderToolCall(view))
}

func TestRenderGolden_DiffNewFile(t *testing.T) {
	withRenderEnv(t, 80)
	view := ToolCallView{
		Name:       "Write",
		PrimaryArg: "src/routes/upload.ts",
		Status:     CallOK,
		Diff: &ToolDiff{
			Added:   2,
			NewFile: true,
			Lines: []DiffLine{
				{Num: 1, Sign: '+', Text: "export function upload() {}"},
				{Num: 2, Sign: '+', Text: ""},
			},
		},
	}
	assertRenderGolden(t, "diff-new-file", RenderToolCall(view))
}

func TestRenderGolden_Plan2of5(t *testing.T) {
	withRenderEnv(t, 80)
	items := []TodoView{
		{Content: "Read the upload handler", Status: TodoCompletedStatus},
		{Content: "Add the retry loop", Status: TodoCompletedStatus},
		{Content: "Wire the retry loop into the route", Status: TodoInProgressStatus},
		{Content: "Add a test", Status: TodoPendingStatus},
		{Content: "Update the docs", Status: TodoPendingStatus},
	}
	assertRenderGolden(t, "plan-2of5", RenderPlan(items, 80))
}

func TestRenderGolden_Plan5of5(t *testing.T) {
	withRenderEnv(t, 80)
	items := []TodoView{
		{Content: "Read the upload handler", Status: TodoCompletedStatus},
		{Content: "Add the retry loop", Status: TodoCompletedStatus},
		{Content: "Wire the retry loop into the route", Status: TodoCompletedStatus},
		{Content: "Add a test", Status: TodoCompletedStatus},
		{Content: "Update the docs", Status: TodoCompletedStatus},
	}
	assertRenderGolden(t, "plan-5of5", RenderPlan(items, 80))
}

func TestRenderGolden_SubagentsRunning(t *testing.T) {
	withRenderEnv(t, 80)
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "a1", Agent: "scout", Description: "survey the auth module", Depth: 1})
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventTool, ID: "a1", ToolName: "grep"})
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "a2", Agent: "scout", Description: "survey the billing module", Depth: 1})
	assertRenderGolden(t, "subagents-running", p.Render(80))
}

func TestRenderGolden_SubagentsFinished(t *testing.T) {
	withRenderEnv(t, 80)
	p := NewSubagentPanelState()
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventStart, ID: "a1", Agent: "scout", Description: "survey the auth module", Depth: 1})
	p.Apply(agent.SubagentEvent{Kind: agent.SubagentEventDone, ID: "a1", ToolCalls: 3, Usage: msg.Usage{TotalTokens: 1200}})
	assertRenderGolden(t, "subagents-finished", p.Render(80))
}

func TestRenderGolden_Note(t *testing.T) {
	withRenderEnv(t, 80)
	assertRenderGolden(t, "note", RenderNote("Reconnected on attempt 2", 80))
}

func TestRenderGolden_Context(t *testing.T) {
	withRenderEnv(t, 80)
	b := commands.ContextBreakdown{
		ModelLabel: "kiln-large",
		Used:       76_000,
		Window:     200_000,
		Segments: []commands.ContextSegment{
			{Label: "System prompt", Tokens: 8_000},
			{Label: "Tools", Tokens: 4_000},
			{Label: "Conversation", Tokens: 64_000},
			{Label: "Free", Tokens: 124_000},
		},
	}
	assertRenderGolden(t, "context", RenderContext(b, 80))
}
