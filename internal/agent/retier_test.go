package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/router"
	"github.com/andrepato/harness/internal/tool"
)

// bigTool returns about 10k tokens of text (40,000 characters): under
// faux-1's 128k tier's tool-output ceiling (15,360 tokens), over a 16k
// window's (2,048).
func bigTool() *tool.Tool {
	return &tool.Tool{
		Name:        "big",
		Description: "returns a lot of text",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		Execute: func(ctx context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			return tool.Text(strings.Repeat("word word word word\n", 2000)), nil
		},
	}
}

// toolResults are the text of every tool result on started's branch,
// oldest first.
func toolResults(t *testing.T, started *Started) []string {
	t.Helper()
	entries, err := started.Lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(entries) - 1; i >= 0; i-- {
		if tr, ok := entries[i].Message.(msg.ToolResultMessage); ok {
			out = append(out, msg.TextOf(tr.Content))
		}
	}
	return out
}

// After Retier to a smaller tier, a tool result is cut at that tier's
// ceiling; before it, the same result went through whole.
func TestRetierTruncatesToolOutputAtTheNewBudget(t *testing.T) {
	reg := newFauxRegistry(t, `
model: faux-1
steps:
  - tool_call: {name: big, args: {}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "first done"
  - tool_call: {name: big, args: {}, id: tc2}
  - on_tool_result: tc2
    then:
      - text: "second done"
`)
	resolved, err := reg.Resolve(fauxprovider.ProviderID, "faux-1")
	if err != nil {
		t.Fatal(err)
	}
	started, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: t.TempDir(), SessionsRoot: t.TempDir(),
		ExtraTools: []*tool.Tool{bigTool()}, ActiveToolNames: []string{"big"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = started.Harness.Close() }()

	if _, err := started.Lane.Prompt(context.Background(), "one", nil); err != nil {
		t.Fatal(err)
	}
	small := budget.TierForWindow(16_384)
	var changed []budget.Tier
	started.OnTierChanged = func(ctx context.Context, tier budget.Tier) { changed = append(changed, tier) }
	Retier(context.Background(), started, small)
	if _, err := started.Lane.Prompt(context.Background(), "two", nil); err != nil {
		t.Fatal(err)
	}

	results := toolResults(t, started)
	if len(results) != 2 {
		t.Fatalf("%d tool results, want 2", len(results))
	}
	if strings.Contains(results[0], "[truncated:") {
		t.Errorf("the first result was truncated under faux-1's tier")
	}
	if !strings.Contains(results[1], "[truncated:") || len(results[1]) > small.ToolOutputTokens*4+200 {
		t.Errorf("the second result (%d chars) was not cut at the new %d-token ceiling", len(results[1]), small.ToolOutputTokens)
	}
	if got := started.Harness.ToolOutputTokens(); got != small.ToolOutputTokens {
		t.Errorf("ToolOutputTokens = %d, want %d", got, small.ToolOutputTokens)
	}
	if got := started.CurrentTier(); got.ContextWindow != 16_384 {
		t.Errorf("CurrentTier window = %d, want 16384", got.ContextWindow)
	}
	if len(changed) != 1 || changed[0].ContextWindow != 16_384 {
		t.Errorf("OnTierChanged calls = %+v, want one for 16384", changed)
	}
}

// routeTo always routes to model id on faux.
type routeTo string

func (r routeTo) Route(_ context.Context, req router.Request) (router.Route, error) {
	return router.Route{Model: provider.Model{Provider: fauxprovider.ProviderID, ID: string(r)}, ThinkingLevel: req.ThinkingLevel}, nil
}

// On kiln/auto, a request routed to a model with another window re-sizes
// the session for it, once; a later request to the same model does not.
func TestRouteRetiersToTheRoutedWindow(t *testing.T) {
	reg := newFauxRegistry(t, `
model: faux-2
steps:
  - text: "one"
  - text: "two"
`)
	faux1, _ := reg.GetModel(fauxprovider.ProviderID, "faux-1")
	faux2, _ := reg.GetModel(fauxprovider.ProviderID, "faux-2")
	targets := router.Targets{Plan: faux1, PlanComplex: faux1, Implement: faux2, Direct: faux2}
	reg.Register(router.NewVirtualProvider(router.AutoModel(targets)))
	var routers router.Set
	routers.Add(router.AutoProvider, router.AutoModelID, routeTo("faux-2"))
	resolved, err := reg.Resolve(router.AutoProvider, router.AutoModelID)
	if err != nil {
		t.Fatal(err)
	}
	started, err := Start(context.Background(), Options{Registry: reg, Resolved: resolved, Cwd: t.TempDir(), SessionsRoot: t.TempDir(), Routers: routers})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = started.Harness.Close() }()
	if got := started.CurrentTier().ContextWindow; got != faux1.ContextWindow {
		t.Fatalf("starting window = %d, want plan's %d", got, faux1.ContextWindow)
	}
	var mu sync.Mutex
	var changed []int
	started.OnTierChanged = func(ctx context.Context, tier budget.Tier) {
		mu.Lock()
		changed = append(changed, tier.ContextWindow)
		mu.Unlock()
	}
	for _, p := range []string{"a", "b"} {
		if _, err := started.Lane.Prompt(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	want := budget.TierForWindow(faux2.ContextWindow)
	if got := started.CurrentTier(); got != want {
		t.Errorf("CurrentTier = %+v, want faux-2's %+v", got, want)
	}
	if got := started.Harness.ToolOutputTokens(); got != want.ToolOutputTokens {
		t.Errorf("ToolOutputTokens = %d, want %d", got, want.ToolOutputTokens)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(changed) != 1 || changed[0] != faux2.ContextWindow {
		t.Errorf("OnTierChanged windows = %v, want one %d", changed, faux2.ContextWindow)
	}
}

// A subagent runs on its own harness: re-sizing the parent leaves the
// subagent's budgets where its own model put them.
func TestRetierLeavesASubagentAlone(t *testing.T) {
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
	resolved, err := reg.Resolve(fauxprovider.ProviderID, "faux-1")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cwd := t.TempDir()
	parent, err := Start(context.Background(), Options{Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Harness.Close() }()
	child, err := Start(context.Background(), Options{Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root, ParentSessionID: parent.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Harness.Close() }()

	Retier(context.Background(), parent, budget.TierForWindow(16_384))

	if got := child.Harness.ToolOutputTokens(); got != resolved.Tier.ToolOutputTokens {
		t.Errorf("subagent ToolOutputTokens = %d, want its own %d", got, resolved.Tier.ToolOutputTokens)
	}
	if got := child.CurrentTier(); got != resolved.Tier {
		t.Errorf("subagent tier = %+v, want its own", got)
	}
	if got := parent.Harness.ToolOutputTokens(); got == resolved.Tier.ToolOutputTokens {
		t.Errorf("parent ToolOutputTokens unchanged at %d", got)
	}
}
