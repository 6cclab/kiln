package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
)

var generalPurposeFixture = agents.Definition{
	Name:        "general-purpose",
	Description: "Researches a question across many files and returns only the conclusion.",
	Prompt:      "You are a research subagent.",
}

func TestTaskToolDispatchesAndReturnsReport(t *testing.T) {
	var seenAgent, seenPrompt string
	dispatch := func(ctx context.Context, agentName, description, prompt string) (TaskDispatchResult, error) {
		seenAgent = agentName
		seenPrompt = prompt
		return TaskDispatchResult{Text: "the answer"}, nil
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, budget.TierForWindow(200_000))

	res := execTool(t, tl, map[string]any{"subagent_type": "general-purpose", "prompt": "find X"})
	text := textOfBlocks(res)
	if text != "the answer" {
		t.Fatalf("text = %q, want %q", text, "the answer")
	}
	if seenAgent != "general-purpose" || seenPrompt != "find X" {
		t.Fatalf("dispatch saw agent=%q prompt=%q", seenAgent, seenPrompt)
	}
}

func TestTaskToolUnknownAgentNamesWhatExists(t *testing.T) {
	dispatch := func(ctx context.Context, agentName, description, prompt string) (TaskDispatchResult, error) {
		t.Fatal("dispatch must not run for an unknown agent")
		return TaskDispatchResult{}, nil
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, budget.TierForWindow(200_000))

	res := execTool(t, tl, map[string]any{"subagent_type": "nope", "prompt": "x"})
	text := textOfBlocks(res)
	if !strings.Contains(text, "general-purpose") {
		t.Fatalf("text = %q, want it to name the available agent", text)
	}
}

func TestTaskToolReportsDispatchFailureAsResultNotError(t *testing.T) {
	dispatch := func(ctx context.Context, agentName, description, prompt string) (TaskDispatchResult, error) {
		return TaskDispatchResult{}, errFake("model unreachable")
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, budget.TierForWindow(200_000))

	res := execTool(t, tl, map[string]any{"subagent_type": "general-purpose", "prompt": "x"})
	if res.IsError {
		// The TS tool never marks this isError either; a failed subagent
		// is a failed tool call whose *text* carries the reason.
		t.Fatal("expected a normal (non-error) result carrying the failure text")
	}
	text := textOfBlocks(res)
	if !strings.Contains(text, "model unreachable") {
		t.Fatalf("text = %q, want it to include the underlying error", text)
	}
}

func TestTaskToolEmptySubagentReportIsNeverBlank(t *testing.T) {
	dispatch := func(ctx context.Context, agentName, description, prompt string) (TaskDispatchResult, error) {
		return TaskDispatchResult{Text: ""}, nil
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, budget.TierForWindow(200_000))

	res := execTool(t, tl, map[string]any{"subagent_type": "general-purpose", "prompt": "x"})
	if textOfBlocks(res) == "" {
		t.Fatal("expected a non-empty placeholder when the subagent said nothing")
	}
}

func TestTaskToolNoPromptGiven(t *testing.T) {
	dispatch := func(ctx context.Context, agentName, description, prompt string) (TaskDispatchResult, error) {
		t.Fatal("dispatch must not run without a prompt")
		return TaskDispatchResult{}, nil
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, budget.TierForWindow(200_000))

	res := execTool(t, tl, map[string]any{"subagent_type": "general-purpose"})
	if !strings.Contains(textOfBlocks(res), "No prompt given") {
		t.Fatalf("text = %q, want a no-prompt message", textOfBlocks(res))
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
