package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/claude/agents"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/tool"
)

func TestKilnToolNameMapsClaudeSpellings(t *testing.T) {
	set := tool.NewSet()
	for _, n := range []string{"bash_background", "bash", "read", "mcp__srv__t"} {
		set.Add(&tool.Tool{Name: n})
	}
	for want, ask := range map[string]string{"bash": "Bash", "read": "Read", "mcp__srv__t": "mcp__srv__t", "bash_background": "bash_background"} {
		got, _, ok := kilnToolName(set, ask)
		if !ok || got != want {
			t.Errorf("kilnToolName(%q) = %q, %v; want %q", ask, got, ok, want)
		}
	}
	if _, _, ok := kilnToolName(set, "Nope"); ok {
		t.Error("an unknown tool resolved")
	}
}

func TestModsCommandServices(t *testing.T) {
	reg := slashcommands.NewRegistry()
	reg.Add(slashcommands.StaticSource(slashcommands.OriginBuiltin, []slashcommands.Command{{
		Name: "hello", Description: "says hello",
		Run: func(_ context.Context, args string) (slashcommands.Result, error) {
			return slashcommands.Result{Output: []string{"hello " + args, "line two"}}, nil
		},
	}}))
	reg.Add(slashcommands.StaticSource(slashcommands.OriginProject, []slashcommands.Command{{Name: "mine", Description: "d", Run: func(context.Context, string) (slashcommands.Result, error) {
		return slashcommands.Result{}, nil
	}}}))
	rt := &modsRuntime{}
	rt.registry.Store(reg)
	var s mods.Services
	modsToolDeps{rt: rt, dispatcher: &agent.Dispatcher{}}.fill(&s)

	got := map[string]string{}
	for _, c := range s.Commands() {
		got[c.Name] = c.Source
	}
	if got["hello"] != "builtin" || got["mine"] != "user" {
		t.Errorf("Commands sources = %v, want hello builtin and mine user", got)
	}
	out, err := s.RunCommand(context.Background(), "hello", "world")
	if err != nil || out.Text != "hello world\nline two" {
		t.Errorf("RunCommand = %+v, %v", out, err)
	}
	if _, err := s.RunCommand(context.Background(), "missing", ""); err == nil {
		t.Error("an unknown command ran")
	}

	// command.describe rewrites show in the list, not in Raw.
	reg.SetDescribe(func(c slashcommands.Command) slashcommands.Command {
		c.Description = "D: " + c.Description
		return c
	})
	if d := reg.List()[0].Description; !strings.HasPrefix(d, "D: ") {
		t.Errorf("List() description = %q, want the rewrite", d)
	}
	if d := reg.Raw()[0].Description; strings.HasPrefix(d, "D: ") {
		t.Errorf("Raw() description = %q, want the source's", d)
	}
}

func TestRegisterAgentMakesItDispatchableAndRefusesShadowing(t *testing.T) {
	d := &agent.Dispatcher{Agents: []agents.Definition{{Name: "Explore"}}}
	var s mods.Services
	modsToolDeps{rt: &modsRuntime{}, dispatcher: d}.fill(&s)
	spec := map[string]any{"name": "scout", "description": "d", "prompt": "p", "tools": []any{"Read"}}
	if err := s.RegisterAgent(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	var found *agents.Definition
	for _, def := range d.AgentList() {
		if def.Name == "scout" {
			found = &def
		}
	}
	if found == nil || found.Prompt != "p" || len(found.Tools) != 1 {
		t.Errorf("registered agent = %+v", found)
	}
	if err := s.RegisterAgent(context.Background(), spec); err == nil {
		t.Error("registering the same name twice succeeded")
	}
	if err := s.RegisterAgent(context.Background(), map[string]any{"name": "Explore", "description": "d", "prompt": "p"}); err == nil {
		t.Error("a mod shadowed a built-in agent")
	}
}
