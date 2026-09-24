package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
)

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

	result, err := d.Dispatch(context.Background(), "general-purpose", "look something up", "find X")
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

func TestDispatchUnknownAgentErrors(t *testing.T) {
	d, _, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n", nil)
	if _, err := d.Dispatch(context.Background(), "nope", "x", "x"); err == nil {
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
	result, err := d.Dispatch(context.Background(), "general-purpose", "x", "go")
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

	result, err := d.Dispatch(context.Background(), "general-purpose", "x", "go")
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

	result, err := d.Dispatch(ctx, "general-purpose", "x", "go")
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

	// GeneralPurpose has no `model:` requested, so Inherited must be false
	// per subagent.ts's `req.agent.model !== undefined` — nothing to fall
	// back FROM.
	if _, err := d.Dispatch(context.Background(), "general-purpose", "x", "go"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(events) == 0 || events[0].Kind != SubagentEventStart {
		t.Fatalf("expected a start event first, got %+v", events)
	}
	if events[0].Inherited {
		t.Error("Inherited = true for an agent with no model field at all")
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
	if _, err := d.Dispatch(context.Background(), "aliased", "x", "go"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !events[0].Inherited {
		t.Error("Inherited = false for an alias that fell back to the parent's model")
	}
}
