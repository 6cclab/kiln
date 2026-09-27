package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session/jsonl"
)

// fakePaidProvider is a second, distinct provider.Provider — faux-1 and
// faux-2 (internal/provider/faux) deliberately share one provider id, so
// they cannot exercise Dispatch's cross-provider paid-model gate (see
// dispatch.go's Dispatch: the gate is only consulted when the resolved
// choice's ProviderID differs from the parent's). This stands in for a
// second real provider (e.g. an Anthropic API key configured alongside a
// local Ollama default) without needing real credentials or network
// access. Its Stream must never be called: every test using it expects
// the gate to block before a session ever starts.
type fakePaidProvider struct {
	id    string
	model provider.Model
}

func (p *fakePaidProvider) ID() string   { return p.id }
func (p *fakePaidProvider) Name() string { return p.id }
func (p *fakePaidProvider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: provider.AuthKindNone}
}
func (p *fakePaidProvider) Models() []provider.Model                { return []provider.Model{p.model} }
func (p *fakePaidProvider) RefreshModels(ctx context.Context) error { return nil }
func (p *fakePaidProvider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	panic("fakePaidProvider.Stream called: the paid-provider gate should have blocked before a subagent session started")
}

// newPaidProvider builds a fakePaidProvider with one model priced at a
// non-zero input rate, so agents.ResolveModel's role lookup can send a
// dispatch to it and Dispatch's cost check (resolved.Model.Cost.Input > 0)
// fires.
func newPaidProvider(id, modelID string) *fakePaidProvider {
	return &fakePaidProvider{
		id: id,
		model: provider.Model{
			ID:            modelID,
			Name:          modelID,
			Api:           provider.ApiAnthropicMessages,
			Provider:      id,
			ContextWindow: 200_000,
			MaxTokens:     8192,
			Cost: provider.ModelCost{
				ModelCostRates: provider.ModelCostRates{Input: 3.0, Output: 15.0},
			},
		},
	}
}

// newParentAndDispatcher starts a parent session against a scripted faux
// server and returns a Dispatcher wired to it, plus the faux server (so a
// caller can extend the script if needed) and Env's cwd.
func newParentAndDispatcher(t *testing.T, script string, gate *permission.Gate) (*Dispatcher, *Started, string) {
	t.Helper()
	reg := newFauxRegistry(t, script)
	resolved := mustResolve(t, reg)
	cwd := t.TempDir()
	root := t.TempDir()

	parent, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root,
	})
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}

	d := &Dispatcher{
		Registry:     reg,
		Parent:       parent,
		Gate:         gate,
		Agents:       []agents.Definition{GeneralPurpose},
		SessionsRoot: root,
		Env:          execenv.New(cwd),
	}
	return d, parent, cwd
}

func TestDispatchCreatesASecondSessionFileBesideTheParents(t *testing.T) {
	d, parent, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"the answer\"\n", nil)

	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Description: "look something up", Prompt: "find X"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.Text != "the answer" {
		t.Fatalf("Text = %q, want %q", result.Text, "the answer")
	}

	dir := filepath.Dir(parent.TranscriptPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d session files under %s, want 2 (parent + subagent)", len(entries), dir)
	}
	sawSubagentFile := false
	for _, e := range entries {
		if full := filepath.Join(dir, e.Name()); full != parent.TranscriptPath {
			sawSubagentFile = true
		}
	}
	if !sawSubagentFile {
		t.Fatal("subagent shares the parent's transcript file")
	}
}

// TestDispatchSetsParentSessionIDOnTheSubagentsHeader checks the header a
// dispatched subagent's own session file carries: ParentSessionID must be
// the dispatching (parent) session's SessionID, not empty — the field
// internal/cli/tui.go's buildRecentSessionRows filters the banner's
// recent-sessions list on, so a subagent's session must be distinguishable
// from a top-level one purely from its own header, without any other
// heuristic.
func TestDispatchSetsParentSessionIDOnTheSubagentsHeader(t *testing.T) {
	d, parent, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"the answer\"\n", nil)

	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Description: "look something up", Prompt: "find X"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	dir := filepath.Dir(parent.TranscriptPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var subagentPath string
	for _, e := range entries {
		if full := filepath.Join(dir, e.Name()); full != parent.TranscriptPath {
			subagentPath = full
		}
	}
	if subagentPath == "" {
		t.Fatalf("no subagent session file found beside %s under %s", parent.TranscriptPath, dir)
	}

	st, err := jsonl.Open(subagentPath, nil)
	if err != nil {
		t.Fatalf("jsonl.Open(%s): %v", subagentPath, err)
	}
	defer st.Close()

	hdr := st.Header()
	if hdr.ParentSessionID != parent.SessionID {
		t.Errorf("subagent header ParentSessionID = %q, want the parent's SessionID %q", hdr.ParentSessionID, parent.SessionID)
	}

	parentHdr, err := jsonl.Open(parent.TranscriptPath, nil)
	if err != nil {
		t.Fatalf("jsonl.Open(parent %s): %v", parent.TranscriptPath, err)
	}
	defer parentHdr.Close()
	if got := parentHdr.Header().ParentSessionID; got != "" {
		t.Errorf("parent (top-level) header ParentSessionID = %q, want empty", got)
	}
}

func TestDispatchUnknownAgentErrors(t *testing.T) {
	d, _, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n", nil)
	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "nope", Description: "x", Prompt: "x"}); err == nil {
		t.Fatal("expected an error for an unknown agent")
	}
}

func TestDispatchReturnsLastNonEmptyAssistantText(t *testing.T) {
	script := `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "final report"
`
	d, _, _ := newParentAndDispatcher(t, script, nil)
	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Description: "x", Prompt: "go"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.Text != "final report" {
		t.Fatalf("Text = %q, want %q", result.Text, "final report")
	}
	if result.ToolCalls != 1 {
		t.Fatalf("ToolCalls = %d, want 1", result.ToolCalls)
	}
}

func TestDispatchSharedGateBlocksToolInsideSubagent(t *testing.T) {
	// A deny rule on the shared gate must apply inside the subagent's own
	// harness: dispatching must never be a way to run a tool the user
	// would otherwise have been asked about.
	gate := permission.NewGate(permission.GateOptions{
		Permissions: settings.Permissions{Deny: []string{"Write"}},
		Mode:        settings.ModeAuto,
	})

	script := `
model: faux-1
steps:
  - tool_call: {name: write, args: {path: "blocked.txt", content: "should never land"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`
	d, _, cwd := newParentAndDispatcher(t, script, gate)
	gate.AddRoot(cwd)

	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Description: "x", Prompt: "go"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.Text != "done" {
		t.Fatalf("Text = %q, want %q (the script still runs past a blocked tool)", result.Text, "done")
	}

	// Verify the mechanism, not a proxy for it: the write tool must never
	// actually have executed.
	if _, err := os.Stat(filepath.Join(cwd, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked.txt exists (or stat failed unexpectedly: %v) — the deny rule did not apply inside the subagent", err)
	}
}

func TestDispatchParentContextCancelAbortsSubagent(t *testing.T) {
	script := `
model: faux-1
steps:
  - delay: 200ms
  - text: "should not be seen"
`
	d, _, _ := newParentAndDispatcher(t, script, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := d.Dispatch(ctx, DispatchRequest{Agent: "general-purpose", Description: "x", Prompt: "go"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.Text == "should not be seen" {
		t.Fatal("subagent ran to completion despite a cancelled parent context")
	}
}

func TestDispatchReportsInheritedModel(t *testing.T) {
	d, parent, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n", nil)

	var events []SubagentEvent
	d.OnEvent = func(ev SubagentEvent) { events = append(events, ev) }

	// GeneralPurpose has no `model:` requested and the task call carries
	// no `model` argument either, so requested collapses to "" and
	// agents.ResolveModel reports ResolveInherited — Inherited is
	// therefore true here. This is a deliberate change from the pre-P2
	// Dispatch(agentName, description, prompt) signature, whose Inherited
	// distinguished "never asked" from "asked and fell back"
	// (subagent.ts's `req.agent.model !== undefined`); the phase brief's
	// resolution algorithm (DispatchRequest.Model, else def.Model, through
	// agents.ResolveModel) does not carry that distinction, and Inherited
	// is now uniformly "landed on the parent's model, whatever the
	// reason" — ResolveKind (SubagentEvent.ModelKind) is what tells the
	// two cases apart, see TestDispatchRoleResolutionKinds.
	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Description: "x", Prompt: "go"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(events) == 0 || events[0].Kind != SubagentEventStart {
		t.Fatalf("expected a start event first, got %+v", events)
	}
	if !events[0].Inherited {
		t.Error("Inherited = false for an agent with no model field and no task model argument")
	}
	if events[0].ModelKind != string(agents.ResolveInherited) {
		t.Errorf("ModelKind = %q, want %q", events[0].ModelKind, agents.ResolveInherited)
	}
	if events[0].ModelID != parent.Model.ID {
		t.Fatalf("ModelID = %q, want the parent's %q", events[0].ModelID, parent.Model.ID)
	}

	// Now with an explicit but unresolvable alias: falls back to the
	// parent, and this time Inherited must be true.
	withAlias := agents.Definition{
		Name: "aliased", Description: "x", Prompt: "y", Model: "opus",
	}
	d.Agents = append(d.Agents, withAlias)
	events = nil
	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "aliased", Description: "x", Prompt: "go"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !events[0].Inherited {
		t.Error("Inherited = false for an alias that fell back to the parent's model")
	}
}

func TestDispatchRoleResolutionKinds(t *testing.T) {
	script := "models:\n" +
		"  faux-1:\n" +
		"    - text: \"hi\"\n" +
		"    - text: \"hi\"\n" +
		"  faux-2:\n" +
		"    - text: \"hi\"\n"
	d, parent, _ := newParentAndDispatcher(t, script, nil)
	d.Roles = map[string]string{"fast": "faux/faux-2"}

	var events []SubagentEvent
	d.OnEvent = func(ev SubagentEvent) { events = append(events, ev) }

	t.Run("a task's model argument naming a role resolves as ResolveRole", func(t *testing.T) {
		events = nil
		if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "go", Model: "fast"}); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if events[0].ModelKind != string(agents.ResolveRole) {
			t.Fatalf("ModelKind = %q, want %q", events[0].ModelKind, agents.ResolveRole)
		}
		if events[0].ModelID != "faux-2" || events[0].ProviderID != "faux" {
			t.Fatalf("resolved to %s/%s, want faux/faux-2", events[0].ProviderID, events[0].ModelID)
		}
		if events[0].Inherited {
			t.Error("Inherited = true for a role that resolved cleanly")
		}
	})

	t.Run("empty/inherit model argument resolves as ResolveInherited", func(t *testing.T) {
		events = nil
		if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "go", Model: "inherit"}); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if events[0].ModelKind != string(agents.ResolveInherited) {
			t.Fatalf("ModelKind = %q, want %q", events[0].ModelKind, agents.ResolveInherited)
		}
		if events[0].ModelID != parent.Model.ID {
			t.Fatalf("ModelID = %q, want the parent's %q", events[0].ModelID, parent.Model.ID)
		}
	})

	t.Run("an unresolvable model argument falls back to the parent", func(t *testing.T) {
		events = nil
		if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "go", Model: "not-a-role"}); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if events[0].ModelKind != string(agents.ResolveFallback) {
			t.Fatalf("ModelKind = %q, want %q", events[0].ModelKind, agents.ResolveFallback)
		}
		if !events[0].Inherited {
			t.Error("Inherited = false for a role name that resolved nothing")
		}
	})
}

func TestDispatchExplicitTaskModelOverridesAgentDefinitionModel(t *testing.T) {
	script := "models:\n" +
		"  faux-1:\n" +
		"    - text: \"hi\"\n" +
		"  faux-2:\n" +
		"    - text: \"hi\"\n"
	d, _, _ := newParentAndDispatcher(t, script, nil)
	d.Roles = map[string]string{"fast": "faux/faux-2"}
	// The agent definition itself requests a role that would resolve
	// differently, so this test can tell whether the task's own `model`
	// argument actually won.
	d.Agents = append(d.Agents, agents.Definition{
		Name: "picky", Description: "x", Prompt: "y", Model: "inherit",
	})

	var events []SubagentEvent
	d.OnEvent = func(ev SubagentEvent) { events = append(events, ev) }

	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "picky", Prompt: "go", Model: "fast"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if events[0].ModelID != "faux-2" {
		t.Fatalf("ModelID = %q, want the task's own model argument (faux-2) to override the definition's %q", events[0].ModelID, "inherit")
	}
}

func TestDispatchGateBlocksCrossProviderPaidDispatch(t *testing.T) {
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
	paid := newPaidProvider("paid", "paid-model")
	reg.Register(paid)

	resolved := mustResolve(t, reg)
	cwd := t.TempDir()
	root := t.TempDir()

	parent, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root,
	})
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}

	gate := permission.NewGate(permission.GateOptions{
		Permissions: settings.Permissions{Deny: []string{"task"}},
		Mode:        settings.ModeAuto,
	})

	d := &Dispatcher{
		Registry:     reg,
		Parent:       parent,
		Gate:         gate,
		Agents:       []agents.Definition{GeneralPurpose},
		Roles:        map[string]string{"heavy": "paid/paid-model"},
		SessionsRoot: root,
		Env:          execenv.New(cwd),
	}

	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "go", Model: "heavy"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.Text == "" || !containsAll(result.Text, "paid/paid-model", "not allowed") {
		t.Fatalf("Text = %q, want a refusal naming paid/paid-model", result.Text)
	}
}

func TestDispatchGateNotConsultedForSameProviderOrFreeModel(t *testing.T) {
	t.Run("same provider, different model (faux-1 -> faux-2)", func(t *testing.T) {
		d, _, _ := newParentAndDispatcher(t, "models:\n  faux-1:\n    - text: \"parent\"\n  faux-2:\n    - text: \"sub\"\n", nil)
		d.Roles = map[string]string{"fast": "faux/faux-2"}
		gate := permission.NewGate(permission.GateOptions{
			Permissions: settings.Permissions{Deny: []string{"task"}},
			Mode:        settings.ModeAuto,
		})
		d.Gate = gate

		result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "go", Model: "fast"})
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if result.Text != "sub" {
			t.Fatalf("Text = %q, want %q — a same-provider role switch must not be gated", result.Text, "sub")
		}
	})

	t.Run("cross provider, free model", func(t *testing.T) {
		reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
		free := newPaidProvider("free-provider", "free-model")
		free.model.Cost = provider.ModelCost{}
		reg.Register(free)

		resolved := mustResolve(t, reg)
		cwd := t.TempDir()
		root := t.TempDir()
		parent, err := Start(context.Background(), Options{Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root})
		if err != nil {
			t.Fatalf("Start parent: %v", err)
		}
		gate := permission.NewGate(permission.GateOptions{
			Permissions: settings.Permissions{Deny: []string{"task"}},
			Mode:        settings.ModeAuto,
		})
		d := &Dispatcher{
			Registry:     reg,
			Parent:       parent,
			Gate:         gate,
			Agents:       []agents.Definition{GeneralPurpose},
			Roles:        map[string]string{"free": "free-provider/free-model"},
			SessionsRoot: root,
			Env:          execenv.New(cwd),
		}
		// free-provider's Stream panics deliberately (see fakePaidProvider),
		// standing in for "a real session actually started". A dispatch
		// with a Deny-everything gate that still reaches Stream proves the
		// gate was never consulted for a free model — the positive of what
		// TestDispatchGateBlocksCrossProviderPaidDispatch proves for a
		// paid one. If the gate wrongly fired here, Dispatch would return
		// the "not allowed" text below without ever panicking.
		reachedStream := func() (reached bool) {
			defer func() {
				if recover() != nil {
					reached = true
				}
			}()
			_, _ = d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "go", Model: "free"})
			return false
		}()
		if !reachedStream {
			t.Fatal("dispatch to a free cross-provider model never reached Stream — the gate must have wrongly blocked it")
		}
	})
}

func TestDispatchUsagePopulated(t *testing.T) {
	d, _, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"the answer\"\n", nil)
	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "go"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.Model == "" {
		t.Fatal("Model is empty, want provider/model of the subagent's session")
	}
	if result.Usage.TotalTokens == 0 && result.Usage.Input == 0 && result.Usage.Output == 0 {
		t.Fatal("Usage is entirely zero, want the subagent's scripted usage")
	}
}

// TestDispatchNestedDepthLimit exercises three levels of subagent
// dispatch — root -> S1 -> S2 -> S3, all synchronous (each dispatch waits
// for its child to finish before continuing its own turn) — to check the
// depth-2 limit and event/model plumbing together:
//
//   - S1, dispatched directly by the root Dispatcher (Depth 0), has a
//     `task` tool and uses it successfully (test a: "a depth-0 subagent's
//     tool set includes task").
//   - S2, dispatched from inside S1 via the child Dispatcher (Depth 1) S1's
//     own `task` tool is wired to, also has one and uses it successfully
//     (test b: "a depth-1 subagent still has task").
//   - S3, dispatched from inside S2 via the child Dispatcher (Depth 2) S2's
//     `task` tool is wired to, has NO `task` tool at all — Dispatch never
//     registers one at Depth 2 — so its own scripted attempt to call one
//     hits the harness's "unknown tool" path instead of ever starting a
//     fourth subagent (test c: "a depth-2 subagent has no task").
//   - S2 and S3 both dispatch with no `model` argument ("inherit"), and
//     both land on faux-2 — the model the subagent that dispatched them
//     actually ran on, not the root's own faux-1 — proving nested dispatch
//     inherits the intermediate subagent's model, not the root's (test d).
//
// All four subagents that do start (root, S1, S2, S3) share faux-2's
// single cursor once S1 is dispatched, since each dispatch is synchronous:
// the assertions below rely on the exact request order this produces (see
// the script's comment).
func TestDispatchNestedDepthLimit(t *testing.T) {
	// faux-2's steps, in the exact order its requests consume them:
	//   1. S1's first turn: tool_call task (tc_s2) -> dispatches S2
	//   2. S2's first turn: tool_call task (tc_s3) -> dispatches S3
	//   3. S3's first turn: tool_call task (tc_s4) -> "task" is unregistered
	//      for S3, so this never reaches Dispatch; the harness answers with
	//      an "unknown tool" error result for tc_s4 and the turn continues.
	//   4. S3's second turn (tool_result for tc_s4, however it resolved):
	//      final text "s3 done".
	//   5. S2's second turn (tool_result for tc_s3 = "s3 done"): final text
	//      "s2 done".
	//   6. S1's second turn (tool_result for tc_s2 = "s2 done"): final text
	//      "s1 done".
	script := "models:\n" +
		"  faux-1:\n" +
		"    - text: \"unused\"\n" +
		"  faux-2:\n" +
		"    - tool_call: {name: task, args: {subagent_type: general-purpose, description: \"d2\", prompt: \"p2\"}, id: tc_s2}\n" +
		"    - tool_call: {name: task, args: {subagent_type: general-purpose, description: \"d3\", prompt: \"p3\"}, id: tc_s3}\n" +
		"    - tool_call: {name: task, args: {subagent_type: general-purpose, description: \"d4\", prompt: \"p4\"}, id: tc_s4}\n" +
		"    - on_tool_result: tc_s4\n" +
		"      then:\n" +
		"        - text: \"s3 done\"\n" +
		"    - on_tool_result: tc_s3\n" +
		"      then:\n" +
		"        - text: \"s2 done\"\n" +
		"    - on_tool_result: tc_s2\n" +
		"      then:\n" +
		"        - text: \"s1 done\"\n"

	d, parent, _ := newParentAndDispatcher(t, script, nil)
	dir := filepath.Dir(parent.TranscriptPath)
	d.Roles = map[string]string{"fast": "faux/faux-2"}

	var events []SubagentEvent
	d.OnEvent = func(ev SubagentEvent) { events = append(events, ev) }

	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "start", Model: "fast"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.Text != "s1 done" {
		t.Fatalf("Text = %q, want %q — the whole S1->S2->S3 chain must have completed", result.Text, "s1 done")
	}

	// Exactly 4 session files: root (parent), S1, S2, S3. A 5th would mean
	// S3 dispatched a fourth subagent despite Depth 2 disallowing it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d session files, want 4 (root + S1 + S2 + S3) — S3 must not have been able to dispatch a 4th subagent", len(entries))
	}

	var starts []SubagentEvent
	for _, ev := range events {
		if ev.Kind == SubagentEventStart {
			starts = append(starts, ev)
		}
	}
	if len(starts) != 3 {
		t.Fatalf("got %d start events, want 3 (S1, S2, S3) — a 4th would mean S3 dispatched despite Depth 2", len(starts))
	}

	// S1 (test a: depth-0 subagent, dispatched by the root Dispatcher whose
	// Depth is 0) is at event Depth 1.
	if starts[0].Depth != 1 {
		t.Fatalf("S1 start event Depth = %d, want 1", starts[0].Depth)
	}
	// S2 (test b: depth-1 subagent, dispatched by the Depth-1 child S1's
	// own task tool is wired to) is at event Depth 2, and — test d —
	// inherited faux-2, S1's own model, not the root's faux-1.
	if starts[1].Depth != 2 {
		t.Fatalf("S2 start event Depth = %d, want 2", starts[1].Depth)
	}
	if starts[1].ModelID != "faux-2" || starts[1].ProviderID != "faux" {
		t.Fatalf("S2 resolved to %s/%s, want faux/faux-2 (S1's own model, not the root's faux-1)", starts[1].ProviderID, starts[1].ModelID)
	}
	if !starts[1].Inherited || starts[1].ModelKind != string(agents.ResolveInherited) {
		t.Fatalf("S2 ModelKind = %q Inherited = %v, want %q / true (nested dispatch with no model argument inherits)", starts[1].ModelKind, starts[1].Inherited, agents.ResolveInherited)
	}
	// S3 (test c: depth-2 subagent, dispatched by the Depth-2 child S2's
	// own task tool is wired to) is at event Depth 3, and also inherited
	// faux-2 from S2.
	if starts[2].Depth != 3 {
		t.Fatalf("S3 start event Depth = %d, want 3", starts[2].Depth)
	}
	if starts[2].ModelID != "faux-2" || starts[2].ProviderID != "faux" {
		t.Fatalf("S3 resolved to %s/%s, want faux/faux-2 (S2's own model)", starts[2].ProviderID, starts[2].ModelID)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// TestDispatchForwardsRunningUsage covers the running-token figure the
// design gives a subagent row that has not finished yet
// (docs/kiln-design-handoff/Terminal.dc.html lines 187-189): the panel
// can only fill that column if Dispatch forwards the subagent session's
// usage before Done, so a SubagentEventUsage must arrive with a nonzero
// total and must describe the subagent's own session rather than the
// parent's.
func TestDispatchForwardsRunningUsage(t *testing.T) {
	d, _, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n", nil)

	var events []SubagentEvent
	d.OnEvent = func(ev SubagentEvent) { events = append(events, ev) }

	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Description: "x", Prompt: "go"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	var usage []SubagentEvent
	doneAt := -1
	for i, ev := range events {
		switch ev.Kind {
		case SubagentEventUsage:
			usage = append(usage, ev)
		case SubagentEventDone:
			doneAt = i
		}
	}
	if len(usage) == 0 {
		t.Fatalf("no SubagentEventUsage was emitted; events = %+v", kindsOf(events))
	}
	if usage[0].Usage.TotalTokens <= 0 {
		t.Errorf("SubagentEventUsage carried TotalTokens = %d, want > 0", usage[0].Usage.TotalTokens)
	}
	for i, ev := range events {
		if ev.Kind == SubagentEventUsage && doneAt >= 0 && i > doneAt {
			t.Errorf("SubagentEventUsage at index %d arrived after Done at %d", i, doneAt)
		}
	}
}

func kindsOf(events []SubagentEvent) []SubagentEventKind {
	out := make([]SubagentEventKind, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}
