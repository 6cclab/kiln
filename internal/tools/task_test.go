package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/tool"
)

var generalPurposeFixture = agents.Definition{
	Name:        "general-purpose",
	Description: "Researches a question across many files and returns only the conclusion.",
	Prompt:      "You are a research subagent.",
}

func TestTaskToolDispatchesAndReturnsReport(t *testing.T) {
	var seenAgent, seenPrompt string
	dispatch := func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error) {
		seenAgent = req.Agent
		seenPrompt = req.Prompt
		return TaskDispatchResult{Text: "the answer"}, nil
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, nil, budget.TierForWindow(200_000))

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
	dispatch := func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error) {
		t.Fatal("dispatch must not run for an unknown agent")
		return TaskDispatchResult{}, nil
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, nil, budget.TierForWindow(200_000))

	res := execTool(t, tl, map[string]any{"subagent_type": "nope", "prompt": "x"})
	text := textOfBlocks(res)
	if !strings.Contains(text, "general-purpose") {
		t.Fatalf("text = %q, want it to name the available agent", text)
	}
}

func TestTaskToolReportsDispatchFailureAsResultNotError(t *testing.T) {
	dispatch := func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error) {
		return TaskDispatchResult{}, errFake("model unreachable")
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, nil, budget.TierForWindow(200_000))

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
	dispatch := func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error) {
		return TaskDispatchResult{Text: ""}, nil
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, nil, budget.TierForWindow(200_000))

	res := execTool(t, tl, map[string]any{"subagent_type": "general-purpose", "prompt": "x"})
	if textOfBlocks(res) == "" {
		t.Fatal("expected a non-empty placeholder when the subagent said nothing")
	}
}

func TestTaskToolNoPromptGiven(t *testing.T) {
	dispatch := func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error) {
		t.Fatal("dispatch must not run without a prompt")
		return TaskDispatchResult{}, nil
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, nil, budget.TierForWindow(200_000))

	res := execTool(t, tl, map[string]any{"subagent_type": "general-purpose"})
	if !strings.Contains(textOfBlocks(res), "No prompt given") {
		t.Fatalf("text = %q, want a no-prompt message", textOfBlocks(res))
	}
}

// schemaProperties decodes a tool's Parameters into its top-level
// "properties" map, for asserting which arguments a schema does or does
// not expose.
func schemaProperties(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decoding schema: %v", err)
	}
	return schema.Properties
}

func TestTaskToolSchemaOmitsModelWhenNoRolesConfigured(t *testing.T) {
	dispatch := func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error) {
		return TaskDispatchResult{}, nil
	}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, nil, budget.TierForWindow(200_000))

	props := schemaProperties(t, tl.Parameters)
	if _, ok := props["model"]; ok {
		t.Fatal("model property present with no roles configured; schema should be unchanged from before roles existed")
	}
}

func TestTaskToolSchemaHasModelEnumWhenRolesConfigured(t *testing.T) {
	dispatch := func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error) {
		return TaskDispatchResult{}, nil
	}
	roles := map[string]string{"fast": "faux/faux-2", "heavy": "faux/faux-1"}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, roles, budget.TierForWindow(200_000))

	props := schemaProperties(t, tl.Parameters)
	modelProp, ok := props["model"].(map[string]any)
	if !ok {
		t.Fatal("model property missing with roles configured")
	}
	enumRaw, ok := modelProp["enum"].([]any)
	if !ok {
		t.Fatal("model property has no enum")
	}
	enum := make([]string, len(enumRaw))
	for i, v := range enumRaw {
		enum[i] = v.(string)
	}
	want := []string{"fast", "heavy", "inherit"}
	if len(enum) != len(want) {
		t.Fatalf("enum = %v, want %v", enum, want)
	}
	for i := range want {
		if enum[i] != want[i] {
			t.Fatalf("enum = %v, want %v", enum, want)
		}
	}
}

func TestTaskToolDescriptionListsRolesWhenConfigured(t *testing.T) {
	dispatch := func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error) {
		return TaskDispatchResult{}, nil
	}
	roles := map[string]string{"fast": "faux/faux-2"}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, roles, budget.TierForWindow(200_000))
	if !strings.Contains(tl.Description, "fast: faux/faux-2") {
		t.Fatalf("description = %q, want it to list the configured role", tl.Description)
	}

	without := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, nil, budget.TierForWindow(200_000))
	if strings.Contains(without.Description, "Available model roles") {
		t.Fatal("description advertises model roles with none configured")
	}
}

func TestTaskToolDispatchReceivesModelAndToolCallID(t *testing.T) {
	var seenModel, seenToolCallID string
	dispatch := func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error) {
		seenModel = req.Model
		seenToolCallID = req.ToolCallID
		return TaskDispatchResult{Text: "ok"}, nil
	}
	roles := map[string]string{"fast": "faux/faux-2"}
	tl := TaskTool(dispatch, []agents.Definition{generalPurposeFixture}, roles, budget.TierForWindow(200_000))

	res, err := tl.Execute(context.Background(),
		[]byte(`{"subagent_type":"general-purpose","prompt":"find X","model":"fast"}`),
		nil, tool.Invocation{ToolCallID: "tc-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if textOfBlocks(res) != "ok" {
		t.Fatalf("text = %q, want %q", textOfBlocks(res), "ok")
	}
	if seenModel != "fast" {
		t.Fatalf("Model = %q, want %q", seenModel, "fast")
	}
	if seenToolCallID != "tc-1" {
		t.Fatalf("ToolCallID = %q, want %q", seenToolCallID, "tc-1")
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
