package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	claudeplugins "github.com/andrepato/harness/internal/claude/plugins"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// --- pure helpers -----------------------------------------------------------

func TestClaudeToolName(t *testing.T) {
	cases := map[string]string{
		"bash":       "Bash",
		"read":       "Read",
		"task":       "Agent",
		"tool_search": "ToolSearch",
		"mcp__squad__status": "mcp__squad__status", // not in the map: passes through
		"":           "",
	}
	for in, want := range cases {
		if got := claudeToolName(in); got != want {
			t.Errorf("claudeToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResultText(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"plain string", json.RawMessage(`"hello"`), "hello"},
		{"object with text", json.RawMessage(`{"text":"from object"}`), "from object"},
		{"object without text falls back to raw JSON", json.RawMessage(`{"other":1}`), `{"other":1}`},
		{"non-string non-object falls back to raw", json.RawMessage(`42`), "42"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resultText(c.raw); got != c.want {
				t.Errorf("resultText(%s) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestModsUsageAndAddUsage(t *testing.T) {
	u := modsUsage(msg.Usage{Input: 10, Output: 5, CacheRead: 2, CacheWrite: 1}, 1000)
	if u.Input != 10 || u.Output != 5 || u.CacheRead != 2 || u.CacheWrite != 1 {
		t.Fatalf("modsUsage copied fields wrong: %+v", u)
	}
	if u.ContextWindow != 1000 || u.ContextTokens != 13 {
		t.Fatalf("modsUsage window/tokens wrong: %+v", u)
	}

	sum := addUsage(mods.Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4},
		mods.Usage{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40, ContextWindow: 99, ContextTokens: 88})
	want := mods.Usage{Input: 11, Output: 22, CacheRead: 33, CacheWrite: 44, ContextWindow: 99, ContextTokens: 88}
	if sum != want {
		t.Fatalf("addUsage = %+v, want %+v", sum, want)
	}
}

func TestUserClaudeSettingsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".claude", "settings.json")
	if got := userClaudeSettingsPath(); got != want {
		t.Fatalf("userClaudeSettingsPath() = %q, want %q", got, want)
	}
}

func TestPluginConfigOptions(t *testing.T) {
	t.Run("missing settings file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if got := pluginConfigOptions(); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		mustWriteClaudeSettings(t, home, "not json")
		if got := pluginConfigOptions(); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("valid pluginConfigs", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		mustWriteClaudeSettings(t, home, `{"pluginConfigs":{"squad":{"options":{"color":"amber"}}}}`)
		got := pluginConfigOptions()
		if got == nil {
			t.Fatal("got nil, want a map")
		}
		squad, ok := got["squad"]
		if !ok {
			t.Fatalf("got %v, missing squad entry", got)
		}
		if squad["color"] != "amber" {
			t.Fatalf("squad options = %v, want color=amber", squad)
		}
	})
}

func mustWriteClaudeSettings(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- newModsRuntime ----------------------------------------------------------

func TestNewModsRuntime_NoModules(t *testing.T) {
	p := claudeplugins.Plugin{Key: "empty@mp", Name: "empty", Root: t.TempDir()}
	if r := newModsRuntime([]claudeplugins.Plugin{p}, "model-x", 1000); r != nil {
		t.Fatalf("newModsRuntime with no hooks modules = %v, want nil", r)
	}
	if r := newModsRuntime(nil, "model-x", 1000); r != nil {
		t.Fatalf("newModsRuntime with no plugins = %v, want nil", r)
	}
}

// echoPluginSource registers a tool and a command from session.start, as a
// real plugin would (squad/savvy-progress use the same on('session.start',
// ...) shape).
const echoPluginSource = `
export const register = (on) => {
  on('session.start', async ($, e, next) => {
    await $.tool.register({ name: 'echo', description: 'Echoes back' });
    await $.command.register({ name: 'hi', description: 'Say hi' });
    return next(e);
  });
};
`

// newTestPlugin writes a minimal real plugin (hooks/hooks.json + a module)
// to a temp directory and returns a claudeplugins.Plugin pointing at it.
func newTestPlugin(t *testing.T, name string) claudeplugins.Plugin {
	t.Helper()
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(`{"modules":["./register.js"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "register.js"), []byte(echoPluginSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return claudeplugins.Plugin{Key: name + "@mp", Name: name, Root: root}
}

func TestNewModsRuntime_WithModule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := newTestPlugin(t, "echoer")
	r := newModsRuntime([]claudeplugins.Plugin{p}, "model-x", 2000)
	if r == nil {
		t.Fatal("newModsRuntime = nil, want a runtime")
	}
	if len(r.modules) != 1 {
		t.Fatalf("modules = %d, want 1", len(r.modules))
	}
	if r.model != "model-x" || r.window != 2000 {
		t.Fatalf("model/window = %q/%d, want model-x/2000", r.model, r.window)
	}
	if r.host == nil || r.host.r != r {
		t.Fatalf("host not wired to the runtime")
	}
}

// --- lifecycle: start, get, close, tool/command listings --------------------

func startedRuntime(t *testing.T) *modsRuntime {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p := newTestPlugin(t, "echoer")
	r := newModsRuntime([]claudeplugins.Plugin{p}, "model-x", 2000)
	if r == nil {
		t.Fatal("newModsRuntime returned nil")
	}
	errs := r.start(context.Background(), t.TempDir())
	if len(errs) != 0 {
		t.Fatalf("start() errors: %v", errs)
	}
	t.Cleanup(r.close)
	return r
}

func TestModsRuntime_StartIsIdempotent(t *testing.T) {
	r := startedRuntime(t)
	e1 := r.get()
	if e1 == nil {
		t.Fatal("get() = nil after start")
	}
	// A second start is a no-op (r.engine already set).
	if errs := r.start(context.Background(), t.TempDir()); errs != nil {
		t.Fatalf("second start() = %v, want nil", errs)
	}
	if r.get() != e1 {
		t.Fatal("second start() replaced the engine")
	}
}

func TestModsRuntime_ToolsAndCommands(t *testing.T) {
	r := startedRuntime(t)

	wantName := "mcp__echoer__echo"
	if !r.isModTool(wantName) {
		t.Fatalf("isModTool(%q) = false, want true", wantName)
	}
	if r.isModTool("mcp__echoer__nope") {
		t.Fatal("isModTool for an unregistered tool = true, want false")
	}

	names := r.toolNames()
	if len(names) != 1 || names[0] != wantName {
		t.Fatalf("toolNames() = %v, want [%s]", names, wantName)
	}

	tools := r.tools("")
	if len(tools) != 1 {
		t.Fatalf("tools('') = %d entries, want 1", len(tools))
	}
	tl := tools[0]
	if tl.Name != wantName || tl.Label != wantName {
		t.Fatalf("tool Name/Label = %q/%q, want %q", tl.Name, tl.Label, wantName)
	}
	if !tl.Concurrent {
		t.Fatal("mod tool should be Concurrent")
	}

	// No hook answers tool.call, so the tool is denied: "<tool>: no hook
	// answered this call" (see internal/mods/events.go BeginTool).
	res, err := tl.Execute(context.Background(), json.RawMessage(`{}`), nil, tool.Invocation{ToolCallID: "tc1"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("Execute result = %+v, want IsError", res)
	}
	if got := msg.TextOf(res.Content); got != wantName+": no hook answered this call" {
		t.Fatalf("Execute error text = %q", got)
	}

	cmds := r.commands()
	if len(cmds) != 1 || cmds[0].Name != "hi" {
		t.Fatalf("commands() = %v, want one command named hi", cmds)
	}
	cmdRes, err := cmds[0].Run(context.Background(), "")
	if err != nil {
		t.Fatalf("command Run error: %v", err)
	}
	if len(cmdRes.Output) != 0 {
		t.Fatalf("command Run output = %v, want none (no command.run hook)", cmdRes.Output)
	}
}

func TestModsRuntime_ToolsNotAvailableWhenEngineMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := newTestPlugin(t, "echoer")
	r := newModsRuntime([]claudeplugins.Plugin{p}, "model-x", 2000)
	// Deliberately never started: r.engine is nil, so toolSet is nil and
	// tools("") returns no tools at all -- this exercises the pre-start
	// nil-safety, not a built tool's Execute (there is nothing to build
	// yet).
	if got := r.tools(""); got != nil {
		t.Fatalf("tools('') before start = %v, want nil", got)
	}
	if got := r.commands(); got != nil {
		t.Fatalf("commands() before start = %v, want nil", got)
	}
	if r.isModTool("mcp__echoer__echo") {
		t.Fatal("isModTool before start = true, want false")
	}
}

// --- nil-receiver safety -----------------------------------------------------

func TestModsRuntime_NilReceiverIsSafe(t *testing.T) {
	var r *modsRuntime
	if r.get() != nil {
		t.Fatal("nil.get() != nil")
	}
	r.close() // must not panic
	if r.isModTool("x") {
		t.Fatal("nil.isModTool() = true")
	}
	if r.toolNames() != nil {
		t.Fatal("nil.toolNames() != nil")
	}
	if r.tools("") != nil {
		t.Fatal("nil.tools() != nil")
	}
	if r.newDriver() != nil {
		t.Fatal("nil.newDriver() != nil")
	}
	if deny := r.BeforeSpawn(context.Background(), "a", "t", &agent.DispatchRequest{}); deny != "" {
		t.Fatalf("nil.BeforeSpawn() = %q, want empty", deny)
	}
}

// --- BeforeSpawn / Done -------------------------------------------------------

func TestModsRuntime_BeforeSpawnAndDone(t *testing.T) {
	r := startedRuntime(t)

	req := &agent.DispatchRequest{Description: "d", Prompt: "do the thing", Model: ""}
	deny := r.BeforeSpawn(context.Background(), "agent-1", "general-purpose", req)
	if deny != "" {
		t.Fatalf("BeforeSpawn deny = %q, want empty", deny)
	}
	// No agent.spawn hook rewrote anything, so the request is unchanged.
	if req.Prompt != "do the thing" || req.Model != "" {
		t.Fatalf("BeforeSpawn mutated the request: %+v", req)
	}

	r.mu.Lock()
	info, ok := r.agents["agent-1"]
	r.mu.Unlock()
	if !ok || info.Status != "running" {
		t.Fatalf("agents[agent-1] = %+v, ok=%v, want status running", info, ok)
	}

	r.Done("agent-1", harness.StatusCompleted, msg.Usage{})
	r.mu.Lock()
	gotStatus := r.agents["agent-1"].Status
	r.mu.Unlock()
	if gotStatus != "completed" {
		t.Fatalf("status after Done(completed) = %q", gotStatus)
	}

	// A second agent that aborts.
	r.BeforeSpawn(context.Background(), "agent-2", "general-purpose", &agent.DispatchRequest{})
	r.Done("agent-2", harness.StatusAborted, msg.Usage{})
	r.mu.Lock()
	gotStatus2 := r.agents["agent-2"].Status
	r.mu.Unlock()
	if gotStatus2 != "killed" {
		t.Fatalf("status after Done(aborted) = %q, want killed", gotStatus2)
	}

	// Done for an id that was never spawned must not panic.
	r.Done("ghost", harness.StatusFailed, msg.Usage{})
}

func TestModsRuntime_BeforeSpawnWithoutEngine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := newTestPlugin(t, "echoer")
	r := newModsRuntime([]claudeplugins.Plugin{p}, "model-x", 2000) // never started
	req := &agent.DispatchRequest{Prompt: "p"}
	if deny := r.BeforeSpawn(context.Background(), "a", "t", req); deny != "" {
		t.Fatalf("BeforeSpawn without a started engine = %q, want empty", deny)
	}
}

// --- modsHost: panes, toasts, agents, settings, session usage ---------------

func TestModsHost_WithoutDriver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := newTestPlugin(t, "echoer")
	r := newModsRuntime([]claudeplugins.Plugin{p}, "model-x", 4096)
	h := r.host

	res := h.OpenPane("echoer", mods.PaneRequest{ID: "p1"})
	if res.IsPlaced {
		t.Fatal("OpenPane without a driver placed the pane")
	}
	if res.Reason == "" {
		t.Fatal("OpenPane without a driver gave no reason")
	}
	if h.ClosePane("p1") {
		t.Fatal("ClosePane without a driver = true")
	}
	if got := h.Panes(); got != nil {
		t.Fatalf("Panes() without a driver = %v, want nil", got)
	}
	h.Toast("echoer", "hello") // must not panic
	h.Invalidate()             // must not panic

	usage := h.SessionUsage()
	if usage.ContextWindow != 4096 {
		t.Fatalf("SessionUsage().ContextWindow = %d, want 4096", usage.ContextWindow)
	}
}

func TestModsHost_Agents(t *testing.T) {
	r := startedRuntime(t)
	r.BeforeSpawn(context.Background(), "a1", "t", &agent.DispatchRequest{Description: "first"})
	r.BeforeSpawn(context.Background(), "a2", "t", &agent.DispatchRequest{Description: "second"})

	got := r.host.Agents()
	if len(got) != 2 {
		t.Fatalf("Agents() = %v, want 2 entries", got)
	}
	if got[0].ID != "a1" || got[1].ID != "a2" {
		t.Fatalf("Agents() order = %v, want a1 then a2", got)
	}
}

func TestModsHost_Settings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := startedRuntimeNoPlugin(t)

	if got := r.host.Settings(); string(got) != "{}" {
		t.Fatalf("Settings() with no file = %q, want {}", got)
	}

	home, _ := os.UserHomeDir()
	mustWriteClaudeSettings(t, home, `{"theme":"dark"}`)
	if got := r.host.Settings(); string(got) != `{"theme":"dark"}` {
		t.Fatalf("Settings() = %q, want the file's content", got)
	}

	mustWriteClaudeSettings(t, home, `not json`)
	if got := r.host.Settings(); string(got) != "{}" {
		t.Fatalf("Settings() with invalid JSON = %q, want {}", got)
	}
}

// startedRuntimeNoPlugin builds and starts a modsRuntime directly (bypassing
// newModsRuntime, which refuses an empty module list) so modsHost methods
// can be exercised without a plugin.
func startedRuntimeNoPlugin(t *testing.T) *modsRuntime {
	t.Helper()
	r := &modsRuntime{
		model:   "model-x",
		window:  1000,
		pending: map[string]mods.ToolHandle{},
		agents:  map[string]*mods.AgentInfo{},
	}
	r.host = &modsHost{r: r}
	if errs := r.start(context.Background(), t.TempDir()); errs != nil {
		t.Fatalf("start() errors: %v", errs)
	}
	t.Cleanup(r.close)
	return r
}

// --- driver wiring: newDriver + modsHost through it --------------------------

func TestModsHost_WithDriver(t *testing.T) {
	r := startedRuntimeNoPlugin(t)
	d := r.newDriver()
	if d == nil {
		t.Fatal("newDriver() = nil")
	}
	t.Cleanup(d.Close)

	h := r.host
	res := h.OpenPane("echoer", mods.PaneRequest{ID: "p1"})
	if !res.IsPlaced {
		t.Fatalf("OpenPane through a driver = %+v, want IsPlaced", res)
	}
	if got := h.Panes(); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("Panes() = %v, want [p1]", got)
	}
	if !h.ClosePane("p1") {
		t.Fatal("ClosePane through a driver = false")
	}
	if got := h.Panes(); len(got) != 0 {
		t.Fatalf("Panes() after close = %v, want none", got)
	}
}

// --- Render / Press -----------------------------------------------------------

func TestModsRuntime_RenderAndPress(t *testing.T) {
	r := startedRuntime(t)
	// No plugin draws at this site, so Render returns no drawings, and
	// Press on an unknown callback id is a safe no-op.
	drawings := r.Render(context.Background(), mods.Site{Component: "AbovePrompt", Surface: "terminal"})
	if len(drawings) != 0 {
		t.Fatalf("Render() = %v, want no drawings", drawings)
	}
	r.Press(mods.Callback{ID: 99999}) // must not panic
}

func TestModsRuntime_RenderPressWithoutEngine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := newTestPlugin(t, "echoer")
	r := newModsRuntime([]claudeplugins.Plugin{p}, "model-x", 1000) // never started
	if got := r.Render(context.Background(), mods.Site{}); got != nil {
		t.Fatalf("Render() before start = %v, want nil", got)
	}
	r.Press(mods.Callback{}) // must not panic
}
