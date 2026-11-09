package mods

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/andrepato/harness/internal/crash"
)

// fsMaxBytes caps one $.fs.read, as Claude Code does (4 MiB per file).
const fsMaxBytes = 4 << 20

// httpMaxBytes caps one $.http.fetch response body kiln reads into memory.
const httpMaxBytes = 16 << 20

// implemented are the $ methods kiln answers in v1. Every other documented
// method (claude-code.d.ts's request map) exists on $ and rejects, naming
// itself, so a mod that calls one fails that call, not the whole hook.
var implemented = map[string]bool{
	"ui.open": true, "ui.close": true, "ui.panes": true, "ui.toast": true, "ui.invalidate": true,
	"ui.resolve": true, "ui.view": true,
	"state.get": true, "state.set": true,
	"clock.now": true, "clock.every": true, "clock.after": true, "clock.sleep": true,
	"env.get": true, "settings.read": true,
	"fs.exists": true, "fs.list": true, "fs.read": true,
	"http.fetch":  true,
	"process.run": true,
	"fs.write":    true, "fs.stat": true, "fs.ancestors": true,
	"store.get": true, "store.set": true, "store.delete": true, "store.keys": true,
	"env.set":        true,
	"mcp.call":       true,
	"model.classify": true, "model.complete": true, "model.fork": true,
	"session.usage": true, "session.cwd": true, "session.id": true,
	"session.root": true, "session.model": true,
	"session.version": true, "session.repo": true, "session.turns": true,
	"agent.list":       true,
	"command.register": true, "tool.register": true,
}

// documented lists the rest of the mod API as Claude Code declares it, plus
// methods its docs name (session.compact/append, prompt.submit, tool.call,
// agent.spawn).
var documented = []string{
	"agent.register", "agent.spawn", "audio.play", "audio.speak",
	"command.list", "config.list",
	"prompt.read", "prompt.submit",
	"session.append", "session.authorize", "session.compact", "session.messages",
	"session.surface", "session.surfaces",
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
		if key := p.name + "\x00" + name; !e.warnedUnimpl[key] {
			e.warnedUnimpl[key] = true
			e.log.Warn(fmt.Sprintf("%s: %s", p.name, msg), "plugin", p.name, "call", name)
		}
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

	case "session.id":
		return e.opts.SessionID, nil

	case "session.root":
		return e.opts.SessionRoot, nil

	case "session.model":
		if e.opts.Model != nil {
			return e.opts.Model(), nil
		}
		return "", nil

	case "session.version":
		return e.toJS(map[string]any{"version": e.opts.Version})

	case "session.repo":
		return e.sessionRepo()

	case "session.turns":
		return e.sessionTurns()

	case "model.complete":
		return e.modelComplete(arg)

	case "model.fork":
		return e.modelFork(arg)

	case "fs.exists":
		_, err := os.Stat(e.fsPath(str(arg(0))))
		return err == nil, nil

	case "fs.list":
		entries, err := os.ReadDir(e.fsPath(str(arg(0))))
		if err != nil {
			return nil, err
		}
		// os.ReadDir sorts by name, as Claude Code's fs.list does.
		out := make([]any, 0, len(entries))
		for _, ent := range entries {
			isLink := ent.Type()&os.ModeSymlink != 0
			kind := "other"
			switch {
			case isLink:
				kind = "other" // a symlink is "other" with isLink, as Claude Code reports.
			case ent.IsDir():
				kind = "dir"
			case ent.Type().IsRegular():
				kind = "file"
			}
			var size int64
			if info, ierr := ent.Info(); ierr == nil && !info.IsDir() {
				size = info.Size()
			}
			out = append(out, map[string]any{"name": ent.Name(), "kind": kind, "size": size, "isLink": isLink})
		}
		return e.toJS(out)

	case "fs.read":
		path := e.fsPath(str(arg(0)))
		as := "text"
		if obj, ok := arg(1).(*goja.Object); ok {
			if s := str(obj.Get("as")); s != "" {
				as = s
			}
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s is a directory", path)
		}
		if info.Size() > fsMaxBytes {
			return nil, fmt.Errorf("file is larger than 4 MiB")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if as == "bytes" {
			return e.toJS(map[string]any{"base64": base64.StdEncoding.EncodeToString(data)})
		}
		return string(data), nil

	case "env.set":
		if goja.IsUndefined(arg(1)) {
			return goja.Undefined(), os.Unsetenv(str(arg(0)))
		}
		return goja.Undefined(), os.Setenv(str(arg(0)), str(arg(1)))

	case "fs.write":
		path := e.fsPath(str(arg(0)))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		return goja.Undefined(), os.WriteFile(path, []byte(str(arg(1))), 0o644)

	case "fs.stat":
		path := e.fsPath(str(arg(0)))
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		isLink := info.Mode()&os.ModeSymlink != 0
		kind := "other" // a symlink is "other" with isLink, as in fs.list.
		switch {
		case isLink:
		case info.IsDir():
			kind = "dir"
		case info.Mode().IsRegular():
			kind = "file"
		}
		out := map[string]any{"kind": kind, "size": info.Size(), "mtimeMs": info.ModTime().UnixMilli(), "isLink": isLink}
		if obj, ok := arg(1).(*goja.Object); ok && truthy(obj.Get("resolve")) {
			if real, rerr := filepath.EvalSymlinks(path); rerr == nil {
				out["realPath"] = real
			}
		}
		return e.toJS(out)

	case "fs.ancestors":
		obj, ok := arg(0).(*goja.Object)
		if !ok {
			return nil, fmt.Errorf("takes { names, of?, below? }")
		}
		var names []string
		if raw, ok := e.fromJS(obj.Get("names")); ok {
			if err := json.Unmarshal(raw, &names); err != nil {
				return nil, fmt.Errorf("names must be an array of strings")
			}
		}
		dir := e.opts.Cwd
		if of := str(obj.Get("of")); of != "" {
			dir = e.fsPath(of)
		}
		dir = filepath.Clean(dir)
		below := ""
		if b := str(obj.Get("below")); b != "" {
			below = filepath.Clean(e.fsPath(b))
		}
		out := []any{}
		for {
			for _, n := range names {
				fp := filepath.Join(dir, n)
				info, err := os.Stat(fp)
				if err != nil || !info.Mode().IsRegular() || info.Size() > fsMaxBytes {
					continue
				}
				data, err := os.ReadFile(fp)
				if err != nil {
					continue
				}
				// Simplification vs Claude Code: @-includes are not expanded; content is the raw file.
				text := string(data)
				out = append(out, map[string]any{"dir": dir, "name": n, "content": text,
					"parts": []any{map[string]any{"path": fp, "content": text}}})
			}
			parent := filepath.Dir(dir)
			if dir == below || parent == dir {
				break
			}
			dir = parent
		}
		return e.toJS(out)

	case "store.get", "store.set", "store.delete", "store.keys":
		return e.storeCall(p.name, name, arg)

	case "http.fetch":
		return e.httpFetch(arg)

	case "process.run":
		return e.processRun(arg)

	case "mcp.call":
		return e.mcpCall(arg)

	case "model.classify":
		return e.modelClassify(arg)

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

// fsPath resolves a $.fs path the way Claude Code does: an absolute path is
// used as given, a relative one against the session's working directory. A
// missing/undefined path ("") means the working directory itself (fs.list()).
func (e *Engine) fsPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.opts.Cwd, p)
}

type storeEntry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// storeCall serves $.store.*: a per-plugin key-value file, insertion-ordered,
// read and rewritten on every call.
func (e *Engine) storeCall(plugin, name string, arg func(int) goja.Value) (any, error) {
	dir := e.opts.CacheDir
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("no cache directory: %v", err)
		}
		dir = filepath.Join(base, "kiln", "mods")
	}
	file := filepath.Join(dir, "store", plugin+".json")
	entries := []storeEntry{}
	if data, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("%s: %v", file, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	save := func() error {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		data, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		return os.WriteFile(file, data, 0o644)
	}
	key := str(arg(0))
	i := -1
	for j, en := range entries {
		if en.Key == key {
			i = j
		}
	}
	switch name {
	case "store.get":
		if i < 0 {
			return goja.Undefined(), nil
		}
		return e.parseJSON(entries[i].Value, false)
	case "store.keys":
		keys := make([]string, 0, len(entries))
		for _, en := range entries {
			keys = append(keys, en.Key)
		}
		return e.toJS(keys)
	case "store.set":
		if raw, ok := e.fromJS(arg(1)); ok {
			if i < 0 {
				entries = append(entries, storeEntry{key, raw})
			} else {
				entries[i].Value = raw
			}
			return goja.Undefined(), save()
		}
	}
	// store.delete, or store.set of undefined.
	if i >= 0 {
		entries = append(entries[:i], entries[i+1:]...)
		return goja.Undefined(), save()
	}
	return goja.Undefined(), nil
}

// httpFetch serves $.http.fetch: an outbound request on a background
// goroutine (network must not block the engine's worker), resolving a promise
// with { status, ok, headers, text }. It matches Claude Code's HttpResponse
// (a text body only; headers lowercased). An auth handle is not supported:
// kiln has no $.session.authorize to mint one.
func (e *Engine) httpFetch(arg func(int) goja.Value) (any, error) {
	vm := e.w.vm
	url := str(arg(0))
	if url == "" {
		return nil, fmt.Errorf("http.fetch(url): url is required")
	}
	method := "GET"
	var body io.Reader
	header := http.Header{}
	socketPath := ""
	if obj, ok := arg(1).(*goja.Object); ok {
		if m := str(obj.Get("method")); m != "" {
			method = strings.ToUpper(m)
		}
		if b := obj.Get("body"); b != nil && !goja.IsUndefined(b) && !goja.IsNull(b) {
			body = strings.NewReader(b.String())
		}
		socketPath = str(obj.Get("socketPath"))
		if h, ok := obj.Get("headers").(*goja.Object); ok {
			for _, k := range h.Keys() {
				header.Set(k, str(h.Get(k)))
			}
		}
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header = header
	client := &http.Client{Timeout: 30 * time.Second}
	if socketPath != "" {
		client.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}}
	}
	promise, resolve, reject := vm.NewPromise()
	go func() {
		defer crash.Guard()
		resp, derr := client.Do(req)
		var (
			status int
			hdrs   = map[string]any{}
			text   string
		)
		if derr == nil {
			data, rerr := io.ReadAll(io.LimitReader(resp.Body, httpMaxBytes))
			resp.Body.Close()
			derr = rerr
			status = resp.StatusCode
			text = string(data)
			for k, v := range resp.Header {
				if len(v) > 0 {
					hdrs[strings.ToLower(k)] = v[0]
				}
			}
		}
		// goja is single-threaded: build the result and settle on the worker.
		e.w.post(func() {
			if derr != nil {
				_ = reject(e.errorValue(derr.Error()))
				return
			}
			val, verr := e.toJS(map[string]any{"status": status, "ok": status >= 200 && status < 300, "headers": hdrs, "text": text})
			if verr != nil {
				_ = reject(e.errorValue(verr.Error()))
				return
			}
			_ = resolve(val)
		})
	}()
	return promise, nil
}

// processRun serves $.process.run: a subprocess on a background goroutine
// (exec must not block the engine's worker), resolving a promise with
// { exitCode, stdout, stderr } to match Claude Code's ProcessRunResult. Like
// Claude Code, a mod's subprocess is not permission-gated: a loaded mod is
// trusted (user decision 2026-11-09). init.env is merged over the inherited
// environment; a failure to start (program not found, bad cwd) rejects, while
// a non-zero exit resolves with that exitCode (-1 when signal-killed, e.g. on
// timeout).
func (e *Engine) processRun(arg func(int) goja.Value) (any, error) {
	vm := e.w.vm
	var argv []string
	if a, ok := arg(0).Export().([]any); ok {
		for _, v := range a {
			argv = append(argv, fmt.Sprint(v))
		}
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("process.run(argv): argv must be a non-empty array")
	}
	dir := e.opts.Cwd
	stdin := ""
	var timeout time.Duration
	env := os.Environ()
	if obj, ok := arg(1).(*goja.Object); ok {
		if c := str(obj.Get("cwd")); c != "" {
			dir = e.fsPath(c)
		}
		stdin = str(obj.Get("stdin"))
		if ms := num(obj.Get("timeoutMs")); ms > 0 {
			timeout = duration(ms)
		}
		if ev, ok := obj.Get("env").(*goja.Object); ok {
			for _, k := range ev.Keys() {
				env = append(env, k+"="+str(ev.Get(k)))
			}
		}
	}
	promise, resolve, reject := vm.NewPromise()
	go func() {
		defer crash.Guard()
		ctx := context.Background()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = env
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var out, errb bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errb
		runErr := cmd.Run()
		exit := 0
		var startErr error
		if runErr != nil {
			var exErr *exec.ExitError
			if errors.As(runErr, &exErr) {
				exit = exErr.ExitCode() // -1 when signal-killed (e.g. timeout)
			} else {
				startErr = runErr // could not start: program not found, bad cwd
			}
		}
		// goja is single-threaded: build the result and settle on the worker.
		e.w.post(func() {
			if startErr != nil {
				_ = reject(e.errorValue(startErr.Error()))
				return
			}
			val, verr := e.toJS(map[string]any{"exitCode": exit, "stdout": out.String(), "stderr": errb.String()})
			if verr != nil {
				_ = reject(e.errorValue(verr.Error()))
				return
			}
			_ = resolve(val)
		})
	}()
	return promise, nil
}

// mcpCall serves $.mcp.call: an MCP tool invocation on a background goroutine
// (the server round-trip must not block the worker), resolving with Claude
// Code's McpToolResult (the MCP CallToolResult: { content, structuredContent?,
// isError }). It rejects when no MCP session is wired, when the tool is
// unknown, or when its server is not connected.
func (e *Engine) mcpCall(arg func(int) goja.Value) (any, error) {
	vm := e.w.vm
	server := str(arg(0))
	tool := str(arg(1))
	if server == "" || tool == "" {
		return nil, fmt.Errorf("mcp.call(server, tool): server and tool are required")
	}
	if e.opts.McpCall == nil {
		return nil, fmt.Errorf("mcp.call is not available: kiln has no MCP session")
	}
	var args map[string]any
	if m, ok := arg(2).Export().(map[string]any); ok {
		args = m
	}
	call := e.opts.McpCall
	promise, resolve, reject := vm.NewPromise()
	go func() {
		defer crash.Guard()
		raw, err := call(context.Background(), server, tool, args)
		// goja is single-threaded: parse and settle on the worker.
		e.w.post(func() {
			if err != nil {
				_ = reject(e.errorValue(err.Error()))
				return
			}
			val, perr := e.parseJSON(raw, true)
			if perr != nil {
				_ = reject(e.errorValue(perr.Error()))
				return
			}
			_ = resolve(val)
		})
	}()
	return promise, nil
}

// modelClassify serves $.model.classify: label text on a background goroutine
// (the model call must not block the worker), resolving the chosen label or
// null when none fits (Claude Code's string? return). It rejects when no model
// session is wired.
func (e *Engine) modelClassify(arg func(int) goja.Value) (any, error) {
	vm := e.w.vm
	text := str(arg(0))
	var labels []string
	if a, ok := arg(1).Export().([]any); ok {
		for _, v := range a {
			labels = append(labels, fmt.Sprint(v))
		}
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("model.classify(text, labels): labels must be a non-empty array")
	}
	if e.opts.Classify == nil {
		return nil, fmt.Errorf("model.classify is not available: kiln has no model session")
	}
	classify := e.opts.Classify
	promise, resolve, reject := vm.NewPromise()
	go func() {
		defer crash.Guard()
		label, err := classify(context.Background(), text, labels)
		// goja is single-threaded: settle on the worker.
		e.w.post(func() {
			if err != nil {
				_ = reject(e.errorValue(err.Error()))
				return
			}
			if label == "" {
				_ = resolve(goja.Null())
				return
			}
			_ = resolve(vm.ToValue(label))
		})
	}()
	return promise, nil
}

// sessionRepo serves $.session.repo: describe the working copy's repository on a
// background goroutine (git must not block the worker), resolving the SessionRepo
// map or null when the cwd is not inside a repo.
func (e *Engine) sessionRepo() (any, error) {
	vm := e.w.vm
	if e.opts.Repo == nil {
		return nil, fmt.Errorf("session.repo is not available in kiln")
	}
	repo := e.opts.Repo
	promise, resolve, reject := vm.NewPromise()
	go func() {
		defer crash.Guard()
		out, err := repo(context.Background())
		// goja is single-threaded: build the result and settle on the worker.
		e.w.post(func() {
			if err != nil {
				_ = reject(e.errorValue(err.Error()))
				return
			}
			if out == nil {
				_ = resolve(goja.Null())
				return
			}
			val, verr := e.toJS(out)
			if verr != nil {
				_ = reject(e.errorValue(verr.Error()))
				return
			}
			_ = resolve(val)
		})
	}()
	return promise, nil
}

// sessionTurns serves $.session.turns: count the prompts the user has sent this
// session on a background goroutine (reading the transcript does I/O), resolving
// the number.
func (e *Engine) sessionTurns() (any, error) {
	vm := e.w.vm
	if e.opts.Turns == nil {
		return nil, fmt.Errorf("session.turns is not available in kiln")
	}
	turns := e.opts.Turns
	promise, resolve, reject := vm.NewPromise()
	go func() {
		defer crash.Guard()
		n, err := turns(context.Background())
		// goja is single-threaded: settle on the worker.
		e.w.post(func() {
			if err != nil {
				_ = reject(e.errorValue(err.Error()))
				return
			}
			_ = resolve(vm.ToValue(n))
		})
	}()
	return promise, nil
}

// modelComplete serves $.model.complete: run a one-shot model request on a
// background goroutine (the model call must not block the worker), resolving the
// ModelCompleteResult (isAnswered + text/usage, or a reason). It rejects only
// when no model session is wired or the request is malformed; an API error is a
// resolved { isAnswered: false, reason: 'api-error', ... } result, as Claude
// Code returns.
func (e *Engine) modelComplete(arg func(int) goja.Value) (any, error) {
	vm := e.w.vm
	if e.opts.Complete == nil {
		return nil, fmt.Errorf("model.complete is not available: kiln has no model session")
	}
	obj, ok := arg(0).(*goja.Object)
	if !ok {
		return nil, fmt.Errorf("model.complete(request): request must be an object")
	}
	req := ModelRequest{
		Model:     str(obj.Get("model")),
		Prompt:    modelBlocks(obj.Get("prompt")),
		System:    modelBlocks(obj.Get("system")),
		MaxTokens: int(num(obj.Get("maxTokens"))),
		Effort:    str(obj.Get("effort")),
	}
	if len(req.Prompt) == 0 {
		return nil, fmt.Errorf("model.complete(request): prompt is required")
	}
	complete := e.opts.Complete
	promise, resolve, reject := vm.NewPromise()
	go func() {
		defer crash.Guard()
		out, err := complete(context.Background(), req)
		e.settleResult(resolve, reject, out, err)
	}()
	return promise, nil
}

// modelFork serves $.model.fork: continue the session's main thread with one more
// prompt on a background goroutine, resolving the ModelCompleteResult (or
// { isAnswered: false, reason: 'nothing-to-fork' } when there is no main thread
// to fork). It rejects only when no model session is wired or prompt is missing.
func (e *Engine) modelFork(arg func(int) goja.Value) (any, error) {
	vm := e.w.vm
	if e.opts.Fork == nil {
		return nil, fmt.Errorf("model.fork is not available: kiln has no model session")
	}
	obj, ok := arg(0).(*goja.Object)
	if !ok {
		return nil, fmt.Errorf("model.fork(request): request must be an object")
	}
	prompt := str(obj.Get("prompt"))
	if prompt == "" {
		return nil, fmt.Errorf("model.fork(request): prompt is required")
	}
	fork := e.opts.Fork
	promise, resolve, reject := vm.NewPromise()
	go func() {
		defer crash.Guard()
		out, err := fork(context.Background(), prompt)
		e.settleResult(resolve, reject, out, err)
	}()
	return promise, nil
}

// settleResult settles a model.complete/fork promise on the worker: reject on a
// wiring error, otherwise resolve the result map (which already carries
// isAnswered and any reason). goja is single-threaded, so this runs on the worker.
func (e *Engine) settleResult(resolve, reject func(any) error, out map[string]any, err error) {
	e.w.post(func() {
		if err != nil {
			_ = reject(e.errorValue(err.Error()))
			return
		}
		val, verr := e.toJS(out)
		if verr != nil {
			_ = reject(e.errorValue(verr.Error()))
			return
		}
		_ = resolve(val)
	})
}

// modelBlocks parses a model.complete prompt or system field: a string becomes
// one block, an array of { text, cache? } becomes that many. Anything else
// (undefined, null) is no blocks.
func modelBlocks(v goja.Value) []ModelTextBlock {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	if arr, ok := v.(*goja.Object); ok && arr.ClassName() == "Array" {
		n := int(num(arr.Get("length")))
		out := make([]ModelTextBlock, 0, n)
		for i := 0; i < n; i++ {
			item := arr.Get(fmt.Sprint(i))
			if obj, ok := item.(*goja.Object); ok {
				out = append(out, ModelTextBlock{Text: str(obj.Get("text")), Cache: truthy(obj.Get("cache"))})
			} else {
				out = append(out, ModelTextBlock{Text: str(item)})
			}
		}
		return out
	}
	return []ModelTextBlock{{Text: v.String()}}
}

// errorValue is an Error object carrying message, for rejecting a promise.
func (e *Engine) errorValue(message string) goja.Value {
	vm := e.w.vm
	if errObj, err := vm.New(vm.Get("Error"), vm.ToValue(message)); err == nil {
		return errObj
	}
	return vm.ToValue(message)
}

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
