package mods

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/dop251/goja"
)

// implemented are the $ methods kiln answers in v1. Every other documented
// method (claude-code.d.ts's request map) exists on $ and rejects, naming
// itself, so a mod that calls one fails that call, not the whole hook.
var implemented = map[string]bool{
	"ui.open": true, "ui.close": true, "ui.panes": true, "ui.toast": true, "ui.invalidate": true,
	"ui.resolve": true, "ui.view": true,
	"state.get": true, "state.set": true,
	"clock.now": true, "clock.every": true, "clock.after": true, "clock.sleep": true,
	"env.get": true, "settings.read": true,
	"session.usage": true, "session.cwd": true,
	"agent.list":       true,
	"command.register": true, "tool.register": true,
}

// documented lists the rest of the mod API as Claude Code 2.1.277 declares
// it, plus methods its docs name (session.compact/send/append,
// prompt.submit, tool.call, agent.spawn).
var documented = []string{
	"agent.register", "agent.spawn", "audio.play", "audio.speak",
	"command.list", "config.list", "env.set",
	"fs.ancestors", "fs.exists", "fs.list", "fs.read", "fs.stat", "fs.write",
	"http.fetch", "mcp.call", "model.classify", "model.complete", "model.fork",
	"process.run", "process.spawn", "prompt.read", "prompt.submit",
	"session.append", "session.authorize", "session.compact", "session.id", "session.messages",
	"session.model", "session.repo", "session.root", "session.send", "session.surface",
	"session.surfaces", "session.turns", "session.version",
	"store.delete", "store.get", "store.keys", "store.set",
	"tool.call", "tool.list", "turn.abort",
	"ui.blit", "ui.log", "ui.notice", "ui.status",
}

func apiNames() []string {
	names := append([]string(nil), documented...)
	for n := range implemented {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// apiCall is every $ method: call(frame, name, ...args). It returns a
// promise, except ui.resolve (the element table) and clock.after/every (a
// Timer), which are synchronous in Claude Code too.
func (e *Engine) apiCall(call goja.FunctionCall) goja.Value {
	f, _ := call.Argument(0).Export().(*frame)
	name := call.Argument(1).String()
	var args []goja.Value
	if len(call.Arguments) > 2 {
		args = call.Arguments[2:]
	}
	arg := func(i int) goja.Value {
		if i < len(args) {
			return args[i]
		}
		return goja.Undefined()
	}
	if f == nil {
		return e.rejected("$ called without a hook")
	}
	p := f.plugin
	vm := e.w.vm
	if !implemented[name] {
		msg := fmt.Sprintf("$.%s is not available in kiln (mods v1)", name)
		e.log.Warn(fmt.Sprintf("%s: %s", p.name, msg), "plugin", p.name, "call", name)
		return e.rejected(msg)
	}
	switch name {
	case "ui.resolve":
		return e.js.table
	case "clock.every", "clock.after":
		return e.startTimer(f, name == "clock.every", arg(0), arg(1))
	}
	v, err := e.answer(f, name, arg)
	if err != nil {
		return e.rejected(fmt.Sprintf("$.%s: %v", name, err))
	}
	return e.promiseOf(vm.ToValue(v))
}

// answer runs one asynchronous $ method; its value becomes the promise's.
func (e *Engine) answer(f *frame, name string, arg func(int) goja.Value) (any, error) {
	p := f.plugin
	vm := e.w.vm
	switch name {
	case "ui.open":
		obj, ok := arg(0).(*goja.Object)
		if !ok {
			return nil, fmt.Errorf("takes { id, title }")
		}
		req := PaneRequest{
			ID:            str(obj.Get("id")),
			Title:         str(obj.Get("title")),
			Focus:         truthy(obj.Get("focus")),
			CloseOnEscape: truthy(obj.Get("closeOnEscape")),
			Rows:          int(num(obj.Get("rows"))),
			Columns:       int(num(obj.Get("columns"))),
		}
		if !nameRule.MatchString(req.ID) {
			return nil, fmt.Errorf("pane id %q: letters, digits, _ and -, up to 64", req.ID)
		}
		if owner, ok := e.paneOwner[req.ID]; ok && owner != p.name && e.paneOpen(req.ID) {
			return nil, fmt.Errorf("pane %q belongs to %s", req.ID, owner)
		}
		res := e.host.OpenPane(p.name, req)
		e.paneOwner[req.ID] = p.name
		e.paneTitle[req.ID] = req.Title
		out := vm.NewObject()
		_ = out.Set("isPlaced", res.IsPlaced)
		if res.Reason != "" {
			_ = out.Set("reason", res.Reason)
		}
		return out, nil

	case "ui.close":
		id := ""
		if obj, ok := arg(0).(*goja.Object); ok {
			id = str(obj.Get("id"))
		} else {
			id = str(arg(0))
		}
		if owner, ok := e.paneOwner[id]; ok && owner != p.name {
			return nil, fmt.Errorf("pane %q belongs to %s", id, owner)
		}
		e.host.ClosePane(id)
		delete(e.paneOwner, id)
		return goja.Undefined(), nil

	case "ui.panes":
		var out []any
		for _, id := range e.host.Panes() {
			if e.paneOwner[id] != p.name {
				continue
			}
			out = append(out, map[string]any{
				"id": id, "title": e.paneTitle[id], "isShown": true, "isFocused": false, "isPlaced": true,
			})
		}
		if out == nil {
			out = []any{}
		}
		return e.toJS(out)

	case "ui.toast":
		text := ""
		if obj, ok := arg(0).(*goja.Object); ok && obj.ClassName() == "Object" {
			text = str(obj.Get("text"))
		} else {
			text = str(arg(0))
		}
		e.host.Toast(p.name, text)
		return goja.Undefined(), nil

	case "ui.invalidate":
		e.host.Invalidate()
		return goja.Undefined(), nil

	case "ui.view":
		return e.toJS(map[string]any{"deny": "kiln has no per-agent transcript view"})

	case "state.get":
		k, err := stateRef(arg(0))
		if err != nil {
			return nil, err
		}
		text, ok := e.state.get(k, f.isRender())
		if !ok {
			return goja.Undefined(), nil
		}
		return e.parseJSON([]byte(text), false)

	case "state.set":
		k, err := stateRef(arg(0))
		if err != nil {
			return nil, err
		}
		if k.plugin != p.name {
			return nil, fmt.Errorf("%s may write only its own state, not %s.%s", p.name, k.plugin, k.key)
		}
		if f.isRender() && !f.settled {
			return nil, fmt.Errorf("a ui.render hook reads $.state and cannot write it; write from a callback or another event's hook")
		}
		value := arg(0).(*goja.Object).Get("value")
		text, ok := e.fromJS(value)
		if e.state.set(k, string(text), ok) {
			e.host.Invalidate()
		}
		return goja.Undefined(), nil

	case "clock.now":
		return e.clock.Now().UnixMilli(), nil

	case "clock.sleep":
		ms := num(arg(0))
		if obj, ok := arg(0).(*goja.Object); ok && obj.ClassName() == "Object" {
			ms = num(obj.Get("ms"))
		}
		promise, resolve, _ := vm.NewPromise()
		e.clock.AfterFunc(duration(ms), func() {
			e.w.post(func() { _ = resolve(goja.Undefined()) })
		})
		return promise, nil

	case "env.get":
		v, ok := e.opts.LookupEnv(str(arg(0)))
		if !ok {
			return goja.Undefined(), nil
		}
		return v, nil

	case "settings.read":
		raw := e.host.Settings()
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		return e.parseJSON(raw, true)

	case "session.usage":
		u := e.host.SessionUsage()
		ctx := map[string]any{"window": u.ContextWindow, "tokens": u.ContextTokens}
		if u.ContextWindow > 0 {
			ctx["percent"] = float64(u.ContextTokens) * 100 / float64(u.ContextWindow)
		}
		return e.toJS(map[string]any{"context": ctx, "rateLimits": []any{}})

	case "session.cwd":
		return e.opts.Cwd, nil

	case "agent.list":
		out := []any{}
		for _, a := range e.host.Agents() {
			out = append(out, map[string]any{"id": a.ID, "type": a.Type, "description": a.Description, "status": a.Status})
		}
		return e.toJS(out)

	case "command.register":
		obj, ok := arg(0).(*goja.Object)
		if !ok {
			return nil, fmt.Errorf("takes { name, description }")
		}
		spec := CommandSpec{
			Plugin:       p.name,
			Name:         str(obj.Get("name")),
			Description:  str(obj.Get("description")),
			ArgumentHint: str(obj.Get("argumentHint")),
		}
		if !nameRule.MatchString(spec.Name) {
			return nil, fmt.Errorf("command name %q: letters, digits, _ and -, up to 64", spec.Name)
		}
		e.mu.Lock()
		for i, c := range e.commands {
			if c.Name == spec.Name && c.Plugin != p.name {
				e.mu.Unlock()
				return nil, fmt.Errorf("%q refused: %s registered it", "/"+spec.Name, c.Plugin)
			}
			if c.Name == spec.Name {
				e.commands = append(e.commands[:i], e.commands[i+1:]...)
				break
			}
		}
		e.commands = append(e.commands, spec)
		e.mu.Unlock()
		return e.toJS(map[string]any{"name": spec.Name})

	case "tool.register":
		obj, ok := arg(0).(*goja.Object)
		if !ok {
			return nil, fmt.Errorf("takes { name, description, inputSchema }")
		}
		name := str(obj.Get("name"))
		if !nameRule.MatchString(name) {
			return nil, fmt.Errorf("tool name %q: letters, digits, _ and -, up to 64", name)
		}
		schema, ok := e.fromJS(obj.Get("inputSchema"))
		if !ok {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		spec := ToolSpec{
			Plugin:      p.name,
			FullName:    "mcp__" + p.name + "__" + name,
			Description: str(obj.Get("description")),
			InputSchema: schema,
			IsDeferred:  truthy(obj.Get("isDeferred")),
		}
		e.mu.Lock()
		for i, t := range e.tools {
			if t.FullName == spec.FullName {
				e.tools = append(e.tools[:i], e.tools[i+1:]...)
				break
			}
		}
		e.tools = append(e.tools, spec)
		e.mu.Unlock()
		return e.toJS(map[string]any{"name": spec.FullName})
	}
	return nil, fmt.Errorf("not implemented")
}

// paneOpen reports whether the host still shows pane id.
func (e *Engine) paneOpen(id string) bool {
	for _, open := range e.host.Panes() {
		if open == id {
			return true
		}
	}
	return false
}

func stateRef(v goja.Value) (stateKey, error) {
	obj, ok := v.(*goja.Object)
	if !ok {
		return stateKey{}, fmt.Errorf("takes { plugin, key }")
	}
	k := stateKey{plugin: str(obj.Get("plugin")), key: str(obj.Get("key"))}
	if k.plugin == "" || k.key == "" {
		return stateKey{}, fmt.Errorf("takes { plugin, key }")
	}
	return k, nil
}

// --- timers ---------------------------------------------------------------------

// timer is a $.clock.after/every timer. Its function runs on the worker; an
// every timer asks for its next period once the function (and the promise
// it returned) settles, so a slow tick never piles up.
type timer struct {
	e      *Engine
	plugin *plugin
	fn     goja.Callable
	period time.Duration
	every  bool
	stop   func() bool
	done   bool
}

func (e *Engine) startTimer(f *frame, every bool, msArg, fnArg goja.Value) goja.Value {
	vm := e.w.vm
	fn, ok := goja.AssertFunction(fnArg)
	if !ok {
		panic(vm.NewTypeError("$.clock.%s(ms, fn): fn is not a function", map[bool]string{true: "every", false: "after"}[every]))
	}
	period := duration(num(msArg))
	if every && period < 10*time.Millisecond {
		period = 10 * time.Millisecond
	}
	t := &timer{e: e, plugin: f.plugin, fn: fn, period: period, every: every}
	f.plugin.timers[t] = true
	t.schedule()
	obj := vm.NewObject()
	_ = obj.Set("cancel", func(goja.FunctionCall) goja.Value { t.cancel(); return goja.Undefined() })
	return obj
}

func (t *timer) schedule() {
	if t.done {
		return
	}
	t.stop = t.e.clock.AfterFunc(t.period, func() { t.e.w.post(t.fire) })
}

func (t *timer) cancel() {
	if t.done {
		return
	}
	t.done = true
	if t.stop != nil {
		t.stop()
	}
	delete(t.plugin.timers, t)
}

func (t *timer) fire() {
	if t.done || t.e.closed.Load() {
		return
	}
	if !t.every {
		t.done = true
		delete(t.plugin.timers, t)
	}
	ret, err := t.fn(goja.Undefined())
	if err != nil {
		t.e.log.Warn(fmt.Sprintf("%s: $.clock timer threw %v", t.plugin.name, jsErr(err)), "plugin", t.plugin.name)
		t.schedule()
		return
	}
	if !t.every {
		return
	}
	t.e.then(ret, func(goja.Value) { t.schedule() }, func(r goja.Value) {
		t.e.log.Warn(fmt.Sprintf("%s: $.clock timer failed: %s", t.plugin.name, reasonText(r)), "plugin", t.plugin.name)
		t.schedule()
	})
}

// --- value helpers ----------------------------------------------------------------

func str(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

func num(v goja.Value) float64 {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return 0
	}
	return v.ToFloat()
}

func truthy(v goja.Value) bool {
	return v != nil && v.ToBoolean()
}

func duration(ms float64) time.Duration {
	if ms != ms || ms < 0 {
		return 0
	}
	return time.Duration(ms * float64(time.Millisecond))
}
