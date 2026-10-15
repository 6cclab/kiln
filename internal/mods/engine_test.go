package mods

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func stateJSON(t *testing.T, e *Engine, plugin, key string) any {
	t.Helper()
	e.sync()
	text, ok := e.state.snapshot(plugin, key)
	if !ok {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("state %s.%s = %q: %v", plugin, key, text, err)
	}
	return v
}

func runCommand(t *testing.T, h *harness, name, args string) string {
	t.Helper()
	text, ok, err := h.e.RunCommand(ctxT(t), name, args)
	if err != nil || !ok {
		t.Fatalf("RunCommand(%s) = %q, %v, %v\nlogs:\n%s", name, text, ok, err, h.logs)
	}
	return text
}

// Two mods on one event nest in load order: the first is outermost, sees
// the event first and the result last, and each next(e) hands the rewrite on.
func TestNextChainOrderAndRewrite(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "chain-a", "chain-b")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)

	if got := runCommand(t, h, "chain", "x"); got != "A(B:x+a)" {
		t.Errorf("command.run through two mods = %q, want A(B:x+a)", got)
	}
	out := h.e.Spawn(ctx, Spawn{AgentID: "a1", Prompt: "p", Description: "d", Type: "general-purpose", Model: "opus"})
	if out.Deny != "" || out.Spawn.Prompt != "p A B" || out.Spawn.Model != "haiku" {
		t.Errorf("Spawn = %+v, want prompt %q and model haiku", out, "p A B")
	}
	if out.Spawn.AgentID != "a1" || out.Spawn.Description != "d" {
		t.Errorf("Spawn changed what no hook rewrote: %+v", out.Spawn)
	}

	// Pinned fields: a changed tool is refused (the hook fails and is
	// skipped); one left out is kept.
	o, handle := h.e.BeginTool(ctx, ToolCall{ID: "t1", Tool: "Pinned", Args: json.RawMessage(`{"a":1}`)})
	handle.Finish(json.RawMessage(`"ok"`), false)
	if o.Handled || string(o.Args) != `{"a":1}` {
		t.Errorf("Pinned: %+v", o)
	}
	if !strings.Contains(h.logs.String(), "chain-a: tool.call hook skipped: threw Error: tool.call: tool is pinned; a rewrite of it is refused") {
		t.Errorf("no pinned refusal logged:\n%s", h.logs)
	}
	o, handle = h.e.BeginTool(ctx, ToolCall{ID: "t2", Tool: "Kept", Args: json.RawMessage(`{"a":1}`)})
	handle.Finish(json.RawMessage(`"ok"`), false)
	if o.Handled || string(o.Args) != `{"a":1,"extra":1}` {
		t.Errorf("Kept: %+v, want the rewrite with tool kept", o)
	}
}

func TestFilterMatching(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "filters")
	ctx := ctxT(t)

	cases := []struct {
		tool, agent  string
		deny, result string
		runs         bool
		args         string
	}{
		{tool: "Bash", deny: "no bash"},
		{tool: "Read", agent: "sub", result: `"listed Read"`},
		{tool: "Grep", result: `"listed Grep"`},
		{tool: "mcp__x__lookup", result: `"regex"`},
		{tool: "Edit", runs: true},
		{tool: "Edit", runs: true, args: `{"tool":"kept","n":12345678901234567890}`},
		{tool: "mcp__y__lookup", runs: true},
	}
	for _, c := range cases {
		args := c.args
		if args == "" {
			args = `{"a":1}`
		}
		o, handle := h.e.BeginTool(ctx, ToolCall{ID: "t", Tool: c.tool, AgentID: c.agent, Args: json.RawMessage(args)})
		switch {
		case c.deny != "":
			if o.Deny != c.deny {
				t.Errorf("%s: deny = %q, want %q", c.tool, o.Deny, c.deny)
			}
		case c.result != "":
			if string(o.Result) != c.result || !o.Handled {
				t.Errorf("%s: outcome = %+v, want result %s", c.tool, o, c.result)
			}
		case c.runs:
			if o.Handled || string(o.Args) != args {
				t.Errorf("%s: outcome = %+v, want it to run with its args byte for byte", c.tool, o)
			}
		}
		handle.Finish(json.RawMessage(`"ok"`), false)
	}

	one := h.e.Render(ctx, Site{Component: "Pane", RequestID: "p1"})
	if len(one) != 1 || joined(one[0].Tree) != "one" {
		t.Errorf("Pane p1 = %+v, want the p1 hook's drawing", one)
	}
	if other := h.e.Render(ctx, Site{Component: "Pane", RequestID: "p2"}); len(other) != 0 {
		t.Errorf("Pane p2 = %+v, want no drawing (no hook matches)", other)
	}
	band := h.e.Render(ctx, Site{Component: "AbovePrompt"})
	if len(band) != 1 || joined(band[0].Tree) != "band" {
		t.Errorf("AbovePrompt = %+v", band)
	}
}

func TestToolCallDenyResultAndPassThrough(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "tools")
	ctx := ctxT(t)
	call := func(cmd, agent string) ToolCall {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		return ToolCall{ID: "toolu_1", Tool: "Bash", Args: args, AgentID: agent}
	}

	o, handle := h.e.BeginTool(ctx, call("rm -rf build", ""))
	if o.Deny != "no deleting" || !o.Handled {
		t.Errorf("deny: %+v", o)
	}
	handle.Finish(json.RawMessage(`"ignored"`), false) // a no-op on a denied call

	o, _ = h.e.BeginTool(ctx, call("date", ""))
	if string(o.Result) != `{"stdout":"today"}` || o.Deny != "" {
		t.Errorf("result: %+v", o)
	}

	// Pass-through: the hook rewrites the arguments, and its code after next
	// runs only once kiln delivers the tool's result.
	o, handle = h.e.BeginTool(ctx, call("ls", ""))
	if o.Handled || string(o.Args) != `{"command":"ls -la"}` {
		t.Fatalf("pass-through: %+v, want args rewritten to ls -la", o)
	}
	if seen := stateJSON(t, h.e, "tools", "seen"); seen != nil {
		t.Fatalf("the hook resumed before the tool finished: %v", seen)
	}
	handle.Finish(json.RawMessage(`"a b c"`), false)
	want := []any{map[string]any{"command": "ls", "text": "a b c", "isError": false}}
	if seen := stateJSON(t, h.e, "tools", "seen"); !reflect.DeepEqual(seen, want) {
		t.Errorf("after Finish the hook saw %v, want %v", seen, want)
	}

	// A deny from the inner hook reaches the outer one through next.
	o, _ = h.e.BeginTool(ctx, call("ls", "blocked"))
	if o.Deny != "inner deny" {
		t.Errorf("inner deny: %+v", o)
	}

	// Interrupted before Finish: core resolves as an error.
	cctx, cancel := context.WithCancel(ctx)
	o, _ = h.e.BeginTool(cctx, call("pwd", ""))
	if o.Handled {
		t.Fatalf("pwd should run: %+v", o)
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		seen, _ := stateJSON(t, h.e, "tools", "seen").([]any)
		if len(seen) == 3 {
			last := seen[2].(map[string]any)
			if last["isError"] != true || last["text"] != "The tool call was interrupted." {
				t.Errorf("interrupted call seen as %v", last)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hook never saw the interrupted result: %v", seen)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTurnStepAsyncGeneratorSeesResult(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "stepper")
	ctx := ctxT(t)

	step := h.e.BeginStep(ctx, StepStart{AgentID: "a1", TurnID: "t1", Index: 0, Model: "m1", Effort: "high", MessageCount: 3})
	if got := stateJSON(t, h.e, "stepper", "steps"); got != nil {
		t.Fatalf("the generator ran past yield* before the request finished: %v", got)
	}
	step.Finish(TurnStep{AgentID: "a1", TurnID: "t1", Model: "m-resp", Answer: "hello",
		ToolUses:   []ToolUse{{Name: "Read", Input: json.RawMessage(`{"file_path":"a"}`)}},
		StopReason: "tool_use", Usage: Usage{Input: 10, Output: 42}})
	want := []any{map[string]any{
		"before": map[string]any{"model": "m1", "effort": "high", "agentId": "a1"},
		"answer": "hello", "tools": []any{"Read"}, "out": float64(42), "model": "m-resp", "stop": "tool_use",
	}}
	if got := stateJSON(t, h.e, "stepper", "steps"); !reflect.DeepEqual(got, want) {
		t.Errorf("steps = %v\nwant    %v\nlogs:\n%s", got, want, h.logs)
	}

	// A request interrupted before it answered: no response, usage null.
	cctx, cancel := context.WithCancel(ctx)
	_ = h.e.BeginStep(cctx, StepStart{TurnID: "t2", Index: 1, Model: "m1"})
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := stateJSON(t, h.e, "stepper", "steps").([]any)
		if len(got) == 2 {
			last := got[1].(map[string]any)
			if last["answer"] != "" || last["out"] != nil || last["stop"] != nil {
				t.Errorf("interrupted step seen as %v", last)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the interrupted step never settled: %v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHookThatThrowsIsSkipped(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "thrower", "answerer")
	h.e.SessionStart(ctxT(t))

	for name, want := range map[string]string{
		"before": "answered before #1",        // threw before next: skipped
		"after":  "answered after #1",         // threw after next: next's result stands
		"caught": "caught throw called=false", // its .catch handler answered
		"shape":  "answered shape #1",         // returned a number: skipped
	} {
		if got := runCommand(t, h, name, ""); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	logs := h.logs.String()
	for _, want := range []string{
		"thrower: command.run hook skipped: threw Error: boom",
		"thrower: command.run hook skipped: threw Error: late",
		"thrower: command.run hook failed (threw Error: guarded); its .catch handler answers",
		"thrower: command.run hook skipped: threw returned int64 where command.run takes an object",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
}

func TestHookTimeoutIsSkipped(t *testing.T) {
	h := newHarness(t, Options{HookBudget: 200 * time.Millisecond, CatchBudget: 100 * time.Millisecond})
	h.load(t, "slow", "answerer")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)

	start := time.Now()
	if got := runCommand(t, h, "hang", ""); got != "answered hang #1" {
		t.Errorf("hang: %q", got)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 3*time.Second {
		t.Errorf("hang settled after %v, want about its 200ms budget", d)
	}
	if got := runCommand(t, h, "spin", ""); got != "answered spin #1" {
		t.Errorf("spin: %q", got)
	}
	// The engine still answers after the watchdog interrupted a busy loop.
	if got := runCommand(t, h, "hang", ""); got != "answered hang #2" {
		t.Errorf("after spin: %q", got)
	}

	// Waiting on next is not the hook's own time: a tool that runs three
	// budgets long still reaches the hook's code after next.
	o, handle := h.e.BeginTool(ctx, ToolCall{ID: "t", Tool: "Slow", Args: json.RawMessage(`{}`)})
	if o.Handled {
		t.Fatalf("Slow should run: %+v", o)
	}
	time.Sleep(600 * time.Millisecond)
	handle.Finish(json.RawMessage(`"done"`), false)
	if got := stateJSON(t, h.e, "slow", "after"); got != "saw done" {
		t.Errorf("after a long next the hook recorded %v\nlogs:\n%s", got, h.logs)
	}

	logs := h.logs.String()
	if !strings.Contains(logs, "slow: command.run hook skipped: timed out") {
		t.Errorf("no timeout line:\n%s", logs)
	}
	if strings.Contains(logs, "slow: tool.call hook skipped") {
		t.Errorf("the tool.call hook was charged for waiting on next:\n%s", logs)
	}
}

func TestStateSetInvalidatesHost(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "statey")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)

	d := h.e.Render(ctx, Site{Component: "Pane", RequestID: "statey"})
	if len(d) != 1 || joined(d[0].Tree) != "shown 0 (write refused)" {
		t.Fatalf("first drawing = %+v, want shown 0 and the render's own write refused", d)
	}
	base := h.host.invalidated()

	if got := runCommand(t, h, "bump", ""); got != "1" {
		t.Errorf("bump = %q", got)
	}
	if n := h.host.invalidated(); n != base+1 {
		t.Errorf("a write to state the drawing read invalidated %d times, want 1", n-base)
	}
	runCommand(t, h, "same", "")
	runCommand(t, h, "hidden", "")
	if n := h.host.invalidated(); n != base+1 {
		t.Errorf("an unchanged value or one no drawing read invalidated (%d calls)", n-base)
	}
	if got := runCommand(t, h, "theirs", ""); !strings.Contains(got, "may write only its own state") {
		t.Errorf("writing another plugin's state: %q", got)
	}
	d = h.e.Render(ctx, Site{Component: "Pane", RequestID: "statey"})
	if len(d) != 1 || joined(d[0].Tree) != "shown 1 (write refused)" {
		t.Errorf("redraw = %q", joined(d[0].Tree))
	}
}

func TestClockEveryFiresAndCancels(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "ticker")
	h.e.SessionStart(ctxT(t))

	h.clock.advance(h.e, 3500*time.Millisecond, 100*time.Millisecond)
	if got := stateJSON(t, h.e, "ticker", "ticks"); got != float64(3) {
		t.Errorf("after 3.5s an every(1000) timer fired %v times, want 3", got)
	}
	if got := stateJSON(t, h.e, "ticker", "once"); got != float64(1) {
		t.Errorf("after(500) fired %v times, want 1", got)
	}
	want := "stopped at " + itoa(h.clock.Now().UnixMilli())
	if got := runCommand(t, h, "stop", ""); got != want {
		t.Errorf("stop = %q, want %q ($.clock.now reads the injected clock)", got, want)
	}
	h.clock.advance(h.e, 3*time.Second, 100*time.Millisecond)
	if got := stateJSON(t, h.e, "ticker", "ticks"); got != float64(3) {
		t.Errorf("a cancelled timer kept firing: %v", got)
	}
	if n := h.clock.pending(); n != 0 {
		t.Errorf("%d timers still pending after cancel", n)
	}
}

func TestRenderDecodesTreeAndCallbacks(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "drawer")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	site := Site{Component: "Pane", RequestID: "drawer", Title: "T", BodyColumns: 40, BodyRows: 10}

	ds := h.e.Render(ctx, site)
	if len(ds) != 1 || ds[0].Plugin != "drawer" || ds[0].Tree == nil {
		t.Fatalf("Render = %+v\nlogs:\n%s", ds, h.logs)
	}
	root := ds[0].Tree
	if root.Type != "Box" || root.Key != "root" || root.Props["flexDirection"] != "column" || root.Props["paddingLeft"] != float64(2) {
		t.Errorf("root = %s %q %v", root.Type, root.Key, root.Props)
	}
	var kinds []string
	for _, c := range root.Children {
		kinds = append(kinds, c.Type+":"+c.Key)
	}
	wantKinds := []string{"Text:", "Text:row-1", "Text:row-2", ":", "Button:go", "Raster:", "Box:", "Text:"}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("children = %v, want %v (false/null dropped, arrays flattened)", kinds, wantKinds)
	}
	if got := joined(&root.Children[0]); got != "Hi T, 0 presses" || root.Children[0].Props["bold"] != true || root.Children[0].Props["color"] != "#ffffff" {
		t.Errorf("title = %q %v", got, root.Children[0].Props)
	}
	if root.Children[3].Text != "YWI=" {
		t.Errorf("a mod's own component returned %q, want btoa('ab')", root.Children[3].Text)
	}
	if root.Children[5].Props["cells"] != "AQID+g==" || root.Children[5].Props["columns"] != float64(2) {
		t.Errorf("Raster props = %v (Uint8Array.toBase64)", root.Children[5].Props)
	}
	if frag := root.Children[6]; frag.Props["flexDirection"] != "column" || joined(&frag) != "héllo" {
		t.Errorf("fragment = %+v (TextEncoder/TextDecoder round trip)", frag)
	}
	if got := joined(&root.Children[7]); got != "40x10 undefined" {
		t.Errorf("props text = %q, want bodyColumns x scroll.bodyRows and no view agent", got)
	}

	button := root.Children[4]
	first, ok := button.Props["onPress"].(Callback)
	if !ok || button.Props["label"] != "go" {
		t.Fatalf("button props = %#v", button.Props)
	}
	h.e.Press(first)
	if got := stateJSON(t, h.e, "drawer", "presses"); got != float64(1) {
		t.Fatalf("press: presses = %v", got)
	}
	ds = h.e.Render(ctx, site)
	if got := joined(&ds[0].Tree.Children[0]); got != "Hi T, 1 presses" {
		t.Errorf("after a press: %q", got)
	}
	second := ds[0].Tree.Children[4].Props["onPress"].(Callback)
	h.e.Press(first) // from the replaced drawing: does nothing
	if got := stateJSON(t, h.e, "drawer", "presses"); got != float64(1) {
		t.Errorf("a stale callback ran: presses = %v", got)
	}
	h.e.Press(second)
	if got := stateJSON(t, h.e, "drawer", "presses"); got != float64(2) {
		t.Errorf("the new callback did not run: presses = %v", got)
	}

	runCommand(t, h, "big", "")
	ds = h.e.Render(ctx, site)
	drawn := joined(ds[0].Tree)
	if n := len([]rune(drawn)); n != MaxTreeText || strings.Count(drawn, "y") != MaxTreeText-90_000 {
		t.Errorf("a 150,000-character tree over two texts drew %d characters (%d of the second), want the first %d", n, strings.Count(drawn, "y"), MaxTreeText)
	}
}

func TestOptionsDefaultsFromUserConfig(t *testing.T) {
	h := newHarness(t, Options{})
	m := fixture(t, "configured")
	m.Options = map[string]any{"loud": true}
	if errs := h.e.Load([]Module{m}); len(errs) > 0 {
		t.Fatal(errs)
	}
	h.e.SessionStart(ctxT(t))
	var got map[string]any
	if err := json.Unmarshal([]byte(runCommand(t, h, "options", "")), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"greeting": "hi", "loud": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("register's options = %v, want %v (defaults filled, configured values win, no default stays unset)", got, want)
	}
}

func TestUnknownApiCallRejectsNotCrash(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "unknown")
	h.e.SessionStart(ctxT(t))

	if got := runCommand(t, h, "fs", ""); got != "$.fs.read is not available in kiln (mods v1)" {
		t.Errorf("$.fs.read = %q", got)
	}
	logs := h.logs.String()
	for _, want := range []string{
		"unknown: $.fs.read is not available in kiln (mods v1)",
		"unknown: $.http.fetch is not available in kiln (mods v1)",
		"unknown: kiln does not raise prompt.submit (mods v1); this hook never runs",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "hook skipped") {
		t.Errorf("a rejected $ call failed its hook:\n%s", logs)
	}
	// Still answering afterwards.
	if got := runCommand(t, h, "fs", ""); got == "" {
		t.Error("engine stopped answering")
	}
}

func TestImportOutsidePluginRootRefused(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("secret.ts", "export const secret = 'outside'\n")
	register := "export const register = (on: any) => { on('session.start', async ($: any, e: any, next: any) => next(e)) }\n"

	cases := []struct {
		name, src, wantErr string
		setup              func(root string)
	}{
		{name: "parent", src: "import { secret } from '../../secret'\nconsole.log(secret)\n" + register, wantErr: "outside the plugin root"},
		{name: "absolute", src: "import { secret } from '" + filepath.Join(dir, "secret") + "'\nconsole.log(secret)\n" + register, wantErr: "outside the plugin root"},
		{name: "symlink", src: "import { secret } from './link'\nconsole.log(secret)\n" + register, wantErr: "outside the plugin root",
			setup: func(root string) {
				if err := os.Symlink(filepath.Join(dir, "secret.ts"), filepath.Join(root, "hooks", "link.ts")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "node", src: "import * as fs from 'node:fs'\nconsole.log(fs)\n" + register, wantErr: "node:fs"},
		{name: "inside", src: "import { local } from './local'\nconsole.log(local)\n" + register,
			setup: func(root string) {
				write(filepath.Join(filepath.Base(root), "hooks", "local.ts"), "export const local = 1\n")
			}},
	}
	var mods []Module
	for _, c := range cases {
		root := filepath.Join(dir, c.name)
		entry := write(filepath.Join(c.name, "hooks", "register.ts"), c.src)
		if c.setup != nil {
			c.setup(root)
		}
		mods = append(mods, Module{Plugin: c.name, Root: root, Path: entry})
	}
	// The module file itself outside the root.
	mods = append(mods, Module{Plugin: "entry", Root: filepath.Join(dir, "inside"), Path: write("stray.ts", register)})

	h := newHarness(t, Options{})
	errs := h.e.Load(mods)
	got := map[string]string{}
	for _, err := range errs {
		msg := err.Error()
		name := strings.TrimPrefix(strings.SplitN(msg, ":", 2)[0], "mod ")
		got[name] = msg
	}
	for _, c := range cases {
		if c.wantErr == "" {
			if msg, failed := got[c.name]; failed {
				t.Errorf("%s: an import inside the root was refused: %s", c.name, msg)
			}
			continue
		}
		if !strings.Contains(got[c.name], c.wantErr) {
			t.Errorf("%s: error %q, want it to contain %q", c.name, got[c.name], c.wantErr)
		}
	}
	if !strings.Contains(got["entry"], "outside the plugin root") {
		t.Errorf("a hooks module outside its root loaded: %q", got["entry"])
	}
}

func TestBundleCacheReusedAndInvalidated(t *testing.T) {
	cache := t.TempDir()
	root := t.TempDir()
	entry := filepath.Join(root, "hooks", "register.ts")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hooks", "dep.ts"), []byte("export const v = 'one'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("import { v } from './dep'\nexport const register = () => v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := bundle(root, entry, cache)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(cache, "*.json"))
	if len(files) != 1 {
		t.Fatalf("cache files = %v", files)
	}
	again, err := bundle(root, entry, cache)
	if err != nil || again != first {
		t.Fatalf("cached bundle differs: %v", err)
	}
	// Changing an imported file (not the entry) rebuilds.
	if err := os.WriteFile(filepath.Join(root, "hooks", "dep.ts"), []byte("export const v = 'two'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := bundle(root, entry, cache)
	if err != nil || !strings.Contains(changed, "two") || strings.Contains(changed, "one") {
		t.Errorf("a changed import was served from the cache:\n%s", changed)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
