package mods

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// copyTree copies a plugin directory (read only at the source) into dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() && (d.Name() == ".in_use" || d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// realMod copies an installed mod from ~/.claude/plugins/cache into a temp
// dir (never touching the original) and returns its Module; it skips the
// test when the mod is not installed.
func realMod(t *testing.T, name, cacheRel string) Module {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	src := filepath.Join(home, ".claude", "plugins", "cache", cacheRel)
	if _, err := os.Stat(filepath.Join(src, "hooks", "register.tsx")); err != nil {
		t.Skipf("%s is not installed at %s", name, src)
	}
	dst := filepath.Join(t.TempDir(), name)
	copyTree(t, src, dst)
	return Module{Plugin: name, Root: dst, Path: filepath.Join(dst, "hooks", "register.tsx")}
}

func TestRealModsLoad(t *testing.T) {
	squad := realMod(t, "squad", "apato-plugins/squad/1.0.0")
	savvy := realMod(t, "savvy-progress", "claude-kit/savvy-progress/1.2.0")

	h := newHarness(t, Options{CacheDir: t.TempDir(), Cwd: "/work"})
	h.host.usage = Usage{ContextWindow: 200_000, ContextTokens: 12_000}
	h.host.settings = json.RawMessage(`{"language":"en"}`)
	if errs := h.e.Load([]Module{squad, savvy}); len(errs) > 0 {
		t.Fatalf("load: %v\n%s", errs, h.logs)
	}
	ctx := ctxT(t)
	h.e.SessionStart(ctx)

	var cmds []string
	for _, c := range h.e.Commands() {
		cmds = append(cmds, c.Plugin+":"+c.Name)
	}
	for _, want := range []string{"squad:squad", "savvy-progress:agents-info"} {
		if !contains(cmds, want) {
			t.Errorf("Commands() = %v, missing %s", cmds, want)
		}
	}
	var tools []string
	for _, tl := range h.e.Tools() {
		tools = append(tools, tl.FullName)
	}
	for _, want := range []string{"mcp__squad__step", "mcp__savvy-progress__progress", "mcp__savvy-progress__step"} {
		if !contains(tools, want) {
			t.Errorf("Tools() = %v, missing %s", tools, want)
		}
	}

	// A subagent starts: squad adds its step note to the prompt and opens its pane.
	h.host.agents = []AgentInfo{{ID: "a1", Type: "Explore", Description: "Map the parser", Status: "running"}}
	out := h.e.Spawn(ctx, Spawn{AgentID: "a1", ToolUseID: "toolu_1", Type: "Explore", Description: "Map the parser",
		Prompt: "Read the parser.", Model: "claude-sonnet-5", ParentModel: "claude-opus-5-5"})
	if out.Deny != "" {
		t.Fatalf("spawn denied: %s", out.Deny)
	}
	if !strings.Contains(out.Spawn.Prompt, "mcp__squad__step") {
		t.Errorf("squad did not add its step note: %q", out.Spawn.Prompt)
	}
	h.e.TurnStart("a1", "t1")
	step := h.e.BeginStep(ctx, StepStart{AgentID: "a1", TurnID: "t1", Model: "claude-sonnet-5", Effort: "high", MessageCount: 1})
	step.Finish(TurnStep{AgentID: "a1", TurnID: "t1", Model: "claude-sonnet-5", Answer: "",
		ToolUses:   []ToolUse{{Name: "mcp__squad__step", Input: json.RawMessage(`{"done":0,"total":3}`)}},
		StopReason: "tool_use", Usage: Usage{Input: 1000, Output: 200, CacheRead: 5000, CacheWrite: 300}})

	o, err := h.e.RunTool(ctx, ToolCall{ID: "toolu_2", Tool: "mcp__squad__step", Args: json.RawMessage(`{"done":1,"total":3,"note":"reading lexer"}`), AgentID: "a1"},
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			t.Fatal("the step tool has no core; squad answers it")
			return nil, nil
		})
	if err != nil || string(o.Result) != `"ok"` {
		t.Fatalf("RunTool(mcp__squad__step) = %+v, %v; want result \"ok\"", o, err)
	}
	// savvy's own step tool answers too.
	o, _ = h.e.RunTool(ctx, ToolCall{ID: "toolu_3", Tool: "mcp__savvy-progress__step", Args: json.RawMessage(`{"done":1,"total":4}`), AgentID: "a1"}, nil)
	if string(o.Result) != `"ok"` {
		t.Errorf("RunTool(mcp__savvy-progress__step) = %+v; want \"ok\"", o)
	}
	// A real tool runs through core; squad and savvy pass it on.
	ran := false
	o, err = h.e.RunTool(ctx, ToolCall{ID: "toolu_4", Tool: "Grep", Args: json.RawMessage(`{"pattern":"x"}`), AgentID: "a1"},
		func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
			ran = true
			return json.RawMessage(`"no matches"`), nil
		})
	if err != nil || !ran || o.Handled {
		t.Errorf("RunTool(Grep) = %+v, ran=%v, err=%v; want it to run", o, ran, err)
	}

	h.clock.advance(h.e, 3*time.Second, 100*time.Millisecond)

	// While the agent runs, both mods draw the band above the prompt.
	band := h.e.Render(ctx, Site{Component: "AbovePrompt", BodyColumns: 100, BodyRows: 3, Surface: "terminal"})
	t.Logf("AbovePrompt drawings: %d", len(band))
	for _, d := range band {
		t.Logf("  %s: %q", d.Plugin, joined(d.Tree))
	}
	if d := drawingOf(band, "squad"); d == nil || d.Tree == nil || !strings.Contains(joined(d.Tree), "1 running") {
		t.Errorf("squad's band = %+v; want \"1 running\"", d)
	}

	pane := h.e.Render(ctx, Site{Component: "Pane", RequestID: "squad", BodyColumns: 52, BodyRows: 30, Surface: "terminal", Title: "Squad"})
	d := drawingOf(pane, "squad")
	if d == nil || d.Tree == nil {
		t.Fatalf("Render(Pane squad) = %+v\nlogs:\n%s", pane, h.logs)
	}
	txt := joined(d.Tree)
	t.Logf("squad pane (running): %q", txt)
	for _, want := range []string{"Map the parser", "Running · 1", "1/3", "reading lexer"} {
		if !strings.Contains(txt, want) {
			t.Errorf("squad pane lacks %q: %q", want, txt)
		}
	}
	if find(d.Tree, "Raster", "") == nil {
		t.Errorf("squad pane has no Raster vehicle on the terminal")
	}

	h.e.TurnComplete(TurnEnd{AgentID: "a1", TurnID: "t1", DurationMs: 4200, Answer: "done", Model: "claude-sonnet-5",
		Usage: Usage{Input: 1000, Output: 400, CacheRead: 5000, CacheWrite: 300}})
	pane = h.e.Render(ctx, Site{Component: "Pane", RequestID: "squad", BodyColumns: 52, BodyRows: 30, Surface: "terminal"})
	d = drawingOf(pane, "squad")
	if d == nil || d.Tree == nil {
		t.Fatalf("squad pane after completion: %+v", pane)
	}
	txt = joined(d.Tree)
	t.Logf("squad pane (finished): %q", txt)
	for _, want := range []string{"Map the parser", "Finished · 1", "✓"} {
		if !strings.Contains(txt, want) {
			t.Errorf("finished squad pane lacks %q: %q", want, txt)
		}
	}

	// Pressing "Finished" collapses the list: the Button's onPress writes state.
	toggle := find(d.Tree, "Button", "Finished")
	if toggle == nil {
		t.Fatalf("no Finished toggle in %q", txt)
	}
	cb, ok := toggle.Props["onPress"].(Callback)
	if !ok {
		t.Fatalf("toggle onPress = %#v, want a Callback", toggle.Props["onPress"])
	}
	before := h.host.invalidated()
	h.e.Press(cb)
	h.e.sync()
	if h.host.invalidated() == before {
		t.Errorf("pressing the toggle did not invalidate")
	}
	pane = h.e.Render(ctx, Site{Component: "Pane", RequestID: "squad", BodyColumns: 52, BodyRows: 30, Surface: "terminal"})
	if d := drawingOf(pane, "squad"); d == nil || !strings.Contains(joined(d.Tree), "▸ Finished") {
		t.Errorf("after the toggle: %q", joined(drawingOf(pane, "squad").Tree))
	}

	// savvy-progress: a flow reported through its progress tool draws its band and pane.
	o, _ = h.e.RunTool(ctx, ToolCall{ID: "toolu_5", Tool: "mcp__savvy-progress__progress",
		Args: json.RawMessage(`{"title":"Port the lexer","phase":"delegate","tasks":[{"title":"Map the parser","tier":"medium"},{"title":"Write tests","tier":"light"}]}`)}, nil)
	if !strings.HasPrefix(string(o.Result), `"ok:`) {
		t.Errorf("progress tool = %+v", o)
	}
	band = h.e.Render(ctx, Site{Component: "AbovePrompt", BodyColumns: 100, BodyRows: 3, Surface: "terminal"})
	if d := drawingOf(band, "savvy-progress"); d == nil || !strings.Contains(joined(d.Tree), "Port the lexer") {
		t.Errorf("savvy band = %+v", d)
	} else {
		t.Logf("savvy band: %q", joined(d.Tree))
	}
	agentsPane := h.e.Render(ctx, Site{Component: "Pane", RequestID: "savvy-agents", BodyColumns: 60, BodyRows: 30, Surface: "terminal"})
	if d := drawingOf(agentsPane, "savvy-progress"); d == nil || !strings.Contains(joined(d.Tree), "Map the parser") || !strings.Contains(joined(d.Tree), "Write tests") {
		t.Errorf("savvy agents pane = %+v\nlogs:\n%s", d, h.logs)
	} else {
		t.Logf("savvy pane: %q", joined(d.Tree))
	}

	// /squad opens the pane through the host; /agents-info toggles savvy's.
	h.host.panes = nil
	text, ok, err := h.e.RunCommand(ctx, "squad", "")
	if err != nil || !ok || text != "Squad panel opened." {
		t.Errorf("RunCommand(squad) = %q, %v, %v", text, ok, err)
	}
	if !contains(h.host.Panes(), "squad") {
		t.Errorf("squad pane not opened: %v", h.host.Panes())
	}
	text, ok, err = h.e.RunCommand(ctx, "agents-info", "")
	if err != nil || !ok || text != "Agents panel opened." {
		t.Errorf("RunCommand(agents-info) = %q, %v, %v", text, ok, err)
	}
	if strings.Contains(h.logs.String(), "hook skipped") {
		t.Errorf("a real mod's hook failed:\n%s", h.logs)
	}
	t.Logf("engine log:\n%s", h.logs)
}

func drawingOf(ds []Drawing, plugin string) *Drawing {
	for i := range ds {
		if ds[i].Plugin == plugin {
			return &ds[i]
		}
	}
	return nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
