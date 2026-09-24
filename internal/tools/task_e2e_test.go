package tools_test

// End-to-end: a real parent session, wired with the `task` tool built by
// tools.TaskTool, dispatches through a real *agent.Dispatcher to a real
// (faux-scripted) subagent session and gets its final text back. This
// lives in an external tools_test package specifically so it can import
// both internal/tools and internal/agent without creating the import
// cycle those two packages must not have between themselves — see
// internal/tools/task.go's header comment.

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

func newFauxRegistry(t *testing.T, scriptYAML string) *provider.Registry {
	t.Helper()
	srv, err := tkfaux.New(tkfaux.Options{ScriptYAML: scriptYAML})
	if err != nil {
		t.Fatalf("tkfaux.New: %v", err)
	}
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("HARNESS_FAUX_ADDR", addr)
	t.Setenv("HARNESS_FAUX_API", "anthropic-messages")

	fp, ok := fauxprovider.New()
	if !ok {
		t.Fatal("fauxprovider.New reported false with HARNESS_FAUX_ADDR set")
	}
	reg := provider.NewRegistry(nil)
	reg.Register(fp)
	return reg
}

func TestTaskToolEndToEndDispatchesToARealSubagent(t *testing.T) {
	// The faux server here always answers with the same scripted turn,
	// regardless of which session (parent or subagent) is asking. That's
	// fine: what this test proves is that the `task` tool's Execute
	// reaches a real agent.Dispatcher, which starts a real subagent
	// session and returns that session's final assistant text.
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"subagent report\"\n")
	resolved, err := reg.Resolve(fauxprovider.ProviderID, fauxprovider.ModelID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	cwd := t.TempDir()
	root := t.TempDir()

	parent, err := agent.Start(context.Background(), agent.Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root,
	})
	if err != nil {
		t.Fatalf("agent.Start parent: %v", err)
	}

	d := &agent.Dispatcher{
		Registry:     reg,
		Parent:       parent,
		Agents:       []agents.Definition{agent.GeneralPurpose},
		SessionsRoot: root,
		Env:          execenv.New(cwd),
	}

	// d.Dispatch returns agent.DispatchResult, a distinct named type from
	// tools.TaskDispatchResult even though the two are field-for-field
	// identical; Go requires an explicit adapter to bridge them (see
	// internal/tools/task.go's header comment, corrected by this test).
	dispatch := func(ctx context.Context, agentName, description, prompt string) (tools.TaskDispatchResult, error) {
		r, err := d.Dispatch(ctx, agentName, description, prompt)
		return tools.TaskDispatchResult{Text: r.Text, ToolCalls: r.ToolCalls, Chars: r.Chars}, err
	}
	taskTool := tools.TaskTool(dispatch, []agents.Definition{agent.GeneralPurpose}, budget.TierForWindow(200_000))

	res, err := taskTool.Execute(context.Background(),
		[]byte(`{"subagent_type":"general-purpose","description":"look something up","prompt":"find X"}`),
		nil, tool.Invocation{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	text := msg.TextOf(res.Content)
	if text != "subagent report" {
		t.Fatalf("task tool text = %q, want %q", text, "subagent report")
	}
}
