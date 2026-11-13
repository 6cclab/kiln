package mods

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// The two events of loading a module: plugin.register, which judges each
// module before its hooks join, and engine.create, the fold that lets a
// module add nouns to $. Also the properties $ carries ($.plugin, the added
// nouns).

// loadBound limits how long a load-time chain may run, hooks and their catch
// handlers included.
func (e *Engine) loadBound() time.Duration { return e.settleTimeout() }

// judge raises plugin.register for one evaluated module (nothing of it is in
// the hook table yet, so the judges are the modules admitted before it) and
// returns the reason a hook refused it, "" when it may join. A chain that
// fails lets the module join, as in Claude Code.
func (e *Engine) judge(m Module, p *plugin, code string) string {
	if !e.Has("plugin.register") {
		return ""
	}
	prov := m.Provenance
	if prov == "" {
		prov = m.Plugin + "@inline"
	}
	ev := map[string]any{
		"name": m.Plugin, "tier": "user", "root": m.Root,
		"provenance": prov, "uses": usesOf(code, p.patterns),
	}
	if m.Version != "" {
		ev["version"] = m.Version
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.loadBound())
	defer cancel()
	res, err := e.Raise(ctx, "plugin.register", ev, OpResult(func(map[string]any) (map[string]any, error) {
		return map[string]any{"allow": true}, nil
	}))
	if err != nil {
		e.log.Warn("mods: plugin.register chain failed; the module joins", "plugin", m.Plugin, "err", err)
		return ""
	}
	return strOf(res, "refuse")
}

var (
	callRe  = regexp.MustCompile(`\$\d*\.([a-z]+)\.([A-Za-z]+)\b`)
	envRe   = regexp.MustCompile(`\$\d*\.env\.(get|set)\(\s*["']([^"']+)["']`)
	stateRe = regexp.MustCompile(`\$\d*\.state\.(get|set)\(\s*\{\s*plugin\s*:\s*["']([^"']+)["']\s*,\s*key\s*:\s*["']([^"']+)["']`)
)

// usesOf is plugin.register's `uses` for a module: the event patterns its on()
// calls named, and what its bundled source spells on $ (`$.noun.method`, the
// literal env names and state refs). A module that renames `$` or builds a
// name at run time is not seen, which Claude Code's scan refuses to load.
func usesOf(code string, patterns []string) map[string]any {
	known := map[string]bool{}
	for _, n := range apiNames() {
		known[n] = true
	}
	calls := map[string]bool{}
	for _, m := range callRe.FindAllStringSubmatch(code, -1) {
		if n := m[1] + "." + m[2]; known[n] {
			calls[n] = true
		}
	}
	uses := map[string]any{
		"events": append([]string{}, patterns...),
		"calls":  sortedKeys(calls),
	}
	reads, writes := map[string]bool{}, map[string]bool{}
	for _, m := range envRe.FindAllStringSubmatch(code, -1) {
		if m[1] == "get" {
			reads[m[2]] = true
		} else {
			writes[m[2]] = true
		}
	}
	if len(reads)+len(writes) > 0 {
		uses["env"] = map[string]any{"reads": sortedKeys(reads), "writes": sortedKeys(writes)}
	}
	sr, sw := map[string]bool{}, map[string]bool{}
	for _, m := range stateRe.FindAllStringSubmatch(code, -1) {
		k := m[2] + "\x00" + m[3]
		if m[1] == "get" {
			sr[k] = true
		} else {
			sw[k] = true
		}
	}
	if len(sr)+len(sw) > 0 {
		uses["state"] = map[string]any{"reads": stateNames(sr), "writes": stateNames(sw)}
	}
	return uses
}

func sortedKeys(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stateNames(m map[string]bool) []any {
	out := []any{}
	for _, k := range sortedKeys(m) {
		plugin, key, _ := strings.Cut(k, "\x00")
		out = append(out, map[string]any{"plugin": plugin, "key": key})
	}
	return out
}

// fold raises engine.create once over the loaded modules and keeps the nouns
// its steps added. A step that replaces or withholds a noun of core's table
// fails, and its plugin unloads; the returned map names those plugins and why.
func (e *Engine) fold() map[string]string {
	if !e.Has("engine.create") {
		return nil
	}
	type answer struct {
		failed map[string]string
		err    error
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.loadBound())
	defer cancel()
	got, err := await(ctx, e, func(send func(answer)) {
		names := []any{}
		for _, p := range e.plugins {
			names = append(names, p.name)
		}
		input, err := e.toJS(map[string]any{"plugins": names})
		if err != nil {
			send(answer{err: err})
			return
		}
		core, err := e.coreTable()
		if err != nil {
			send(answer{err: err})
			return
		}
		c := e.newChain("engine.create", func(goja.Value) goja.Value { return e.promiseOf(core) })
		e.runChain(c, input, func(v goja.Value, err error) {
			failed := map[string]string{}
			for name, why := range e.createFailed {
				failed[name] = why
				e.unload(name)
			}
			e.createFailed = map[string]string{}
			if err != nil {
				send(answer{failed: failed, err: err})
				return
			}
			if obj, ok := v.(*goja.Object); ok {
				for _, k := range obj.Keys() {
					if core.Get(k) == nil {
						e.nouns[k] = obj.Get(k)
					}
				}
			}
			send(answer{failed: failed})
		})
	})
	if err != nil {
		e.log.Warn("mods: engine.create fold did not finish; no nouns were added", "err", err)
		return nil
	}
	if got.err != nil {
		e.log.Warn("mods: engine.create fold failed; no nouns were added", "err", got.err)
	}
	for name, why := range got.failed {
		e.log.Warn("mods: engine.create unloaded a plugin", "plugin", name, "reason", why)
	}
	return got.failed
}

// coreTable is `$` as core builds it: every method of the API, as the steps
// of the fold see it from next(e). Its methods are the stubs of a call made
// outside a hook, which reject. Worker-only.
func (e *Engine) coreTable() (*goja.Object, error) {
	if e.core != nil {
		return e.core, nil
	}
	v, err := e.js.makeDollar(goja.Undefined(), e.js.apiCall, goja.Undefined(), e.js.apiNames)
	if err != nil {
		return nil, err
	}
	e.core = v.ToObject(e.w.vm)
	return e.core, nil
}

// createViolation is why an engine.create step's result breaks the fold's
// rules ("" when it keeps every noun of core's table as it was; it may add
// nouns). In the user tier a step may not replace or withhold one.
func (e *Engine) createViolation(v goja.Value) string {
	core, err := e.coreTable()
	if err != nil {
		return ""
	}
	obj, ok := v.(*goja.Object)
	if !ok {
		return ""
	}
	for _, k := range core.Keys() {
		got := obj.Get(k)
		if got == nil || !got.StrictEquals(core.Get(k)) {
			return fmt.Sprintf("engine.create replaced or withheld $.%s: a step may only add nouns", k)
		}
	}
	return ""
}

// dollarFor is the `$` one hook dispatch sees: the empty table at
// engine.create (`$` exists after the fold), else the API with $.plugin and
// every noun the fold added. Worker-only.
func (e *Engine) dollarFor(f *frame, dollar goja.Value) goja.Value {
	vm := e.w.vm
	if f.c != nil && f.c.event == "engine.create" {
		return vm.NewObject()
	}
	obj := dollar.ToObject(vm)
	if f.plugin != nil {
		pl := vm.NewObject()
		_ = pl.Set("name", f.plugin.name)
		_ = pl.Set("root", f.plugin.root)
		if _, err := e.js.deepFreeze(goja.Undefined(), pl); err == nil {
			_ = obj.Set("plugin", pl)
		}
	}
	for k, v := range e.nouns {
		_ = obj.Set(k, v)
	}
	return obj
}
