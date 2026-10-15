package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// This file is what kiln calls: one method per event kiln raises, each
// building the payload claude-code.d.ts declares, running the chain, and
// reading back what the hooks decided.

// SessionStart raises session.start ({ cwd, surface: "terminal",
// isInteractive: true }) and waits for every hook: mods register their
// commands and tools here, so Commands and Tools are complete when it returns.
func (e *Engine) SessionStart(ctx context.Context) {
	_, err := await(ctx, e, func(send func(struct{})) {
		input, err := e.toJS(map[string]any{"cwd": e.opts.Cwd, "surface": "terminal", "isInteractive": true})
		if err != nil {
			send(struct{}{})
			return
		}
		cwd := e.opts.Cwd
		c := e.newChain("session.start", func(goja.Value) goja.Value {
			v, _ := e.toJS(map[string]any{"cwd": cwd})
			return v
		})
		e.runChain(c, input, func(_ goja.Value, err error) {
			if err != nil {
				e.log.Warn("mods: session.start failed", "err", err)
			}
			send(struct{}{})
		})
	})
	if err != nil {
		e.log.Warn("mods: session.start did not settle", "err", err)
	}
}

// Commands returns the commands mods registered, in registration order.
func (e *Engine) Commands() []CommandSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]CommandSpec(nil), e.commands...)
}

// Tools returns the tools mods registered, in registration order.
func (e *Engine) Tools() []ToolSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]ToolSpec(nil), e.tools...)
}

// RunCommand raises command.run ({ command, args }) for a command a mod
// registered. ok is true when a hook answered it; text is the answer's
// text. A name no mod registered returns ok false without raising anything.
func (e *Engine) RunCommand(ctx context.Context, name, args string) (string, bool, error) {
	registered := false
	for _, c := range e.Commands() {
		if c.Name == name {
			registered = true
			break
		}
	}
	if !registered {
		return "", false, nil
	}
	type answer struct {
		text string
		ok   bool
		err  error
	}
	got, err := await(ctx, e, func(send func(answer)) {
		input, err := e.toJS(map[string]any{"command": name, "args": args})
		if err != nil {
			send(answer{err: err})
			return
		}
		reachedCore := false
		c := e.newChain("command.run", func(goja.Value) goja.Value {
			reachedCore = true
			v, _ := e.toJS(map[string]any{})
			return v
		})
		e.runChain(c, input, func(v goja.Value, err error) {
			if err != nil {
				send(answer{err: err})
				return
			}
			if reachedCore {
				send(answer{})
				return
			}
			text := ""
			if obj, ok := v.(*goja.Object); ok {
				text = str(obj.Get("text"))
			}
			send(answer{text: text, ok: true})
		})
	})
	if err != nil {
		return "", false, err
	}
	return got.text, got.ok, got.err
}

// ToolHandle finishes a tool call BeginTool let run.
type ToolHandle interface {
	// Finish delivers the tool's result: it resolves the core every hook
	// that called next is waiting on, and returns once the chain settled
	// (bounded by the hooks' budgets). Calling it on a call a hook answered
	// or denied, or twice, does nothing.
	Finish(result json.RawMessage, isError bool)
}

type toolHandle struct {
	e       *Engine
	settled chan struct{}

	// Worker-only.
	reached     bool
	finished    bool
	resolve     func(any) error
	corePromise goja.Value
	coreValue   goja.Value
	stopCtx     func() bool
}

func (h *toolHandle) Finish(result json.RawMessage, isError bool) {
	if h == nil || h.e == nil {
		return
	}
	h.e.w.post(func() { h.finish(result, isError, "") })
	select {
	case <-h.settled:
	case <-h.e.w.done:
	case <-time.After(h.e.settleTimeout()):
		h.e.log.Warn("mods: tool.call chain did not settle after the tool finished")
	}
}

// finish resolves core with { result, text } (or { isError: true, result,
// text }); worker-only.
func (h *toolHandle) finish(result json.RawMessage, isError bool, abortText string) {
	if !h.reached || h.finished || h.resolve == nil {
		return
	}
	h.finished = true
	if h.stopCtx != nil {
		h.stopCtx()
	}
	e := h.e
	out := map[string]any{}
	if abortText != "" {
		out["result"] = abortText
		out["text"] = abortText
		out["isError"] = true
	} else {
		var parsed any
		text := string(result)
		if len(result) == 0 {
			parsed = nil
			text = ""
		} else if err := json.Unmarshal(result, &parsed); err != nil {
			parsed = string(result)
		} else if s, ok := parsed.(string); ok {
			text = s
		}
		out["result"] = parsed
		out["text"] = text
		if isError {
			out["isError"] = true
		}
	}
	v, err := e.toJS(out)
	if err != nil {
		v = goja.Undefined()
	}
	h.coreValue = v
	_ = h.resolve(v)
}

// toolReserved are tool.call's own keys beside the tool's arguments.
var toolReserved = map[string]bool{"tool": true, "tool_use_id": true, "agentId": true, "consent": true}

// BeginTool runs the tool.call chain (the event is the tool's arguments
// spread beside tool, tool_use_id and, in a subagent, agentId) up to its
// core and returns as soon as the outcome is known:
//
//   - a hook answered ({ result }): Result holds it as JSON, Handled is true;
//   - a hook denied ({ deny }): Deny holds the reason, Handled is true;
//   - the chain reached core: run the tool with Args (a hook may have
//     rewritten them), then call handle.Finish with its result. Hooks
//     awaiting next resume then; a different result they return after that
//     is not applied in v1 (logged at debug).
//
// A tool a mod registered (mcp__<plugin>__<name>) has no core of its own:
// when no hook answers it, it is denied. If ctx ends before Finish, core
// resolves as an interrupted error. On a closed engine the tool just runs.
func (e *Engine) BeginTool(ctx context.Context, call ToolCall) (ToolOutcome, ToolHandle) {
	h := &toolHandle{e: e, settled: make(chan struct{})}
	runAsIs := ToolOutcome{Args: call.Args}
	if e.closed.Load() || e.init != nil {
		close(h.settled)
		return runAsIs, h
	}
	isModTool := false
	for _, t := range e.Tools() {
		if t.FullName == call.Tool {
			isModTool = true
			break
		}
	}
	outcome := make(chan ToolOutcome, 1)
	var once sync.Once
	send := func(o ToolOutcome) { once.Do(func() { outcome <- o }) }
	posted := e.w.post(func() {
		vm := e.w.vm
		var input goja.Value
		args, err := e.parseJSON(orEmptyObject(call.Args), false)
		argsObj, isObj := args.(*goja.Object)
		if err != nil || !isObj || argsObj.ClassName() != "Object" {
			argsObj = vm.NewObject()
		}
		ev := vm.NewObject()
		for _, k := range argsObj.Keys() {
			if !toolReserved[k] {
				_ = ev.Set(k, argsObj.Get(k))
			}
		}
		_ = ev.Set("tool", call.Tool)
		_ = ev.Set("tool_use_id", call.ID)
		if call.AgentID != "" {
			_ = ev.Set("agentId", call.AgentID)
		}
		input, _ = e.js.deepFreeze(goja.Undefined(), ev)

		c := e.newChain("tool.call", func(in goja.Value) goja.Value {
			if isModTool {
				v, _ := e.toJS(map[string]any{"deny": fmt.Sprintf("%s: no hook answered this call", call.Tool)})
				return v
			}
			if h.reached {
				return h.corePromise
			}
			h.reached = true
			p, resolve, _ := vm.NewPromise()
			h.resolve = resolve
			h.corePromise = vm.ToValue(p)
			h.stopCtx = context.AfterFunc(ctx, func() {
				e.w.post(func() { h.finish(nil, true, "The tool call was interrupted.") })
			})
			args := call.Args
			if !in.StrictEquals(input) {
				// A hook rewrote the event: the tool runs with its arguments.
				args = e.stripReserved(in)
			}
			send(ToolOutcome{Args: args})
			return h.corePromise
		})
		e.runChain(c, input, func(v goja.Value, err error) {
			defer close(h.settled)
			if h.reached {
				if err != nil {
					e.log.Debug("mods: tool.call chain rejected after the tool ran", "tool", call.Tool, "err", err)
				} else if h.coreValue == nil || !v.StrictEquals(h.coreValue) {
					e.log.Debug("mods: a tool.call hook changed the result after next; kiln does not apply it (v1)", "tool", call.Tool)
				}
				return
			}
			if err != nil {
				e.log.Warn("mods: tool.call chain rejected; the tool runs as called", "tool", call.Tool, "err", err)
				send(runAsIs)
				return
			}
			send(e.toolOutcome(v, call))
		})
	})
	if !posted {
		close(h.settled)
		return runAsIs, h
	}
	select {
	case o := <-outcome:
		return o, h
	case <-ctx.Done():
		return ToolOutcome{Deny: "The tool call was interrupted.", Handled: true}, h
	case <-e.w.done:
		return runAsIs, h
	}
}

// toolOutcome reads a tool.call result that did not come from kiln's core.
func (e *Engine) toolOutcome(v goja.Value, call ToolCall) ToolOutcome {
	obj, ok := v.(*goja.Object)
	if !ok {
		return ToolOutcome{Deny: fmt.Sprintf("%s: a mod answered with no result", call.Tool), Handled: true}
	}
	if d := obj.Get("deny"); d != nil && !goja.IsUndefined(d) {
		return ToolOutcome{Deny: d.String(), Handled: true}
	}
	if r := obj.Get("result"); r != nil {
		raw, ok := e.fromJS(r)
		if !ok {
			raw = json.RawMessage(`null`)
		}
		return ToolOutcome{Result: raw, Handled: true}
	}
	return ToolOutcome{Deny: fmt.Sprintf("%s: a mod answered with neither result nor deny", call.Tool), Handled: true}
}

// stripReserved is the tool's arguments from a tool.call event, as JSON.
func (e *Engine) stripReserved(in goja.Value) json.RawMessage {
	obj, ok := in.(*goja.Object)
	if !ok {
		return json.RawMessage(`{}`)
	}
	out := e.w.vm.NewObject()
	for _, k := range obj.Keys() {
		if !toolReserved[k] {
			_ = out.Set(k, obj.Get(k))
		}
	}
	raw, ok := e.fromJS(out)
	if !ok {
		return json.RawMessage(`{}`)
	}
	return raw
}

func orEmptyObject(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

// RunTool is BeginTool and Finish in one call, for a caller that can block
// while the tool runs: core runs the real tool when the chain reaches it.
func (e *Engine) RunTool(ctx context.Context, call ToolCall, core func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)) (ToolOutcome, error) {
	o, h := e.BeginTool(ctx, call)
	if o.Handled {
		return o, nil
	}
	res, err := core(ctx, o.Args)
	if err != nil {
		msg, _ := json.Marshal(err.Error())
		h.Finish(msg, true)
		return o, err
	}
	h.Finish(res, false)
	return o, nil
}

// Spawn raises agent.spawn before a subagent starts. The event carries
// tool_use_id, prompt, description, subagentType, provider, model (when the
// call named one), parentModel, parentAgentId, background and fork. Core
// resolves to { model, agentId: s.AgentID }, so kiln assigns the agent's id
// before calling and should pass Model as the model it will run on.
//
// The outcome is the event as the hooks passed it to core (a rewritten
// Prompt, Description, Type or Model), or Deny. A hook that answers without
// calling next started no agent, so that is a Deny too.
func (e *Engine) Spawn(ctx context.Context, s Spawn) SpawnOutcome {
	got, err := await(ctx, e, func(send func(SpawnOutcome)) {
		ev := map[string]any{
			"tool_use_id":  s.ToolUseID,
			"prompt":       s.Prompt,
			"description":  s.Description,
			"subagentType": s.Type,
			"provider":     map[string]any{"plugin": "engine", "tier": "core"},
			"parentModel":  s.ParentModel,
			"background":   s.Background,
			"fork":         false,
		}
		if s.Model != "" {
			ev["model"] = s.Model
		}
		if s.ParentAgentID != "" {
			ev["parentAgentId"] = s.ParentAgentID
		}
		input, err := e.toJS(ev)
		if err != nil {
			send(SpawnOutcome{Spawn: s})
			return
		}
		reached := false
		final := s
		c := e.newChain("agent.spawn", func(in goja.Value) goja.Value {
			reached = true
			if obj, ok := in.(*goja.Object); ok {
				final.Prompt = str(obj.Get("prompt"))
				final.Description = str(obj.Get("description"))
				final.Type = str(obj.Get("subagentType"))
				final.Model = str(obj.Get("model"))
			}
			model := final.Model
			if model == "" {
				model = s.ParentModel
			}
			v, _ := e.toJS(map[string]any{"model": model, "agentId": s.AgentID})
			return v
		})
		e.runChain(c, input, func(v goja.Value, err error) {
			if err != nil {
				e.log.Warn("mods: agent.spawn chain rejected; the agent starts as called", "err", err)
				send(SpawnOutcome{Spawn: s})
				return
			}
			if obj, ok := v.(*goja.Object); ok {
				if d := obj.Get("deny"); d != nil && !goja.IsUndefined(d) {
					send(SpawnOutcome{Spawn: s, Deny: d.String()})
					return
				}
			}
			if !reached {
				send(SpawnOutcome{Spawn: s, Deny: "a mod answered agent.spawn without starting the agent"})
				return
			}
			send(SpawnOutcome{Spawn: final})
		})
	})
	if err != nil {
		e.log.Warn("mods: agent.spawn did not settle; the agent starts as called", "err", err)
		return SpawnOutcome{Spawn: s}
	}
	return got
}

// TurnStart raises turn.start ({ text: "", turnId, agentId? }) and waits
// for its hooks (bounded by their budgets).
func (e *Engine) TurnStart(agentID, turnID string) {
	ev := map[string]any{"text": "", "turnId": turnID}
	if agentID != "" {
		ev["agentId"] = agentID
	}
	e.raise("turn.start", ev, map[string]any{"turnId": turnID})
}

// TurnComplete raises turn.complete ({ answer, durationMs, isAborted,
// turnId, agentId?, usage?, reason }) and waits for its hooks. reason is
// "aborted" when IsAborted, "error" when IsFailed, else "answer"; usage is
// left out when the turn reported none.
func (e *Engine) TurnComplete(end TurnEnd) {
	reason := "answer"
	switch {
	case end.IsAborted:
		reason = "aborted"
	case end.IsFailed:
		reason = "error"
	}
	ev := map[string]any{
		"answer":     end.Answer,
		"durationMs": end.DurationMs,
		"isAborted":  end.IsAborted,
		"turnId":     end.TurnID,
		"reason":     reason,
	}
	if end.AgentID != "" {
		ev["agentId"] = end.AgentID
	}
	result := map[string]any{"text": ""}
	if u := turnUsage(end.Usage, end.Model); u != nil {
		ev["usage"] = u
		result["usage"] = u
	}
	e.raise("turn.complete", ev, result)
}

// raise runs an observe-only event and waits for it to settle.
func (e *Engine) raise(event string, ev, coreResult map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), e.settleTimeout())
	defer cancel()
	_, err := await(ctx, e, func(send func(struct{})) {
		input, err := e.toJS(ev)
		if err != nil {
			send(struct{}{})
			return
		}
		c := e.newChain(event, func(goja.Value) goja.Value {
			v, _ := e.toJS(coreResult)
			return v
		})
		e.runChain(c, input, func(_ goja.Value, err error) {
			if err != nil {
				e.log.Warn("mods: "+event+" chain rejected", "err", err)
			}
			send(struct{}{})
		})
	})
	if err != nil && err != ErrClosed {
		e.log.Warn("mods: "+event+" did not settle", "err", err)
	}
}

// turnUsage is a TurnUsage as the API reports it, or nil for none.
func turnUsage(u Usage, model string) map[string]any {
	if u == (Usage{}) && model == "" {
		return nil
	}
	return map[string]any{
		"input_tokens":                u.Input,
		"output_tokens":               u.Output,
		"cache_read_input_tokens":     u.CacheRead,
		"cache_creation_input_tokens": u.CacheWrite,
		"model":                       model,
	}
}

// StepStart is a turn.step event before the model request: kiln knows the
// model and effort it is about to ask with.
type StepStart struct {
	AgentID      string
	TurnID       string
	Index        int
	Model        string
	Effort       string
	MessageCount int
}

// StepHandle finishes a model request BeginStep began.
type StepHandle interface {
	// Finish delivers the request's result: it resolves the chain's core
	// (every hook's yield* next(e)), and returns once the chain settled
	// (bounded by the hooks' budgets). A second call does nothing.
	Finish(TurnStep)
}

type stepHandle struct {
	e       *Engine
	settled chan struct{}
	start   StepStart

	// Worker-only.
	reached  bool
	finished bool
	resolve  func(any) error
	core     goja.Value
	stopCtx  func() bool
}

func (h *stepHandle) Finish(step TurnStep) {
	if h == nil || h.e == nil {
		return
	}
	h.e.w.post(func() { h.finish(&step) })
	select {
	case <-h.settled:
	case <-h.e.w.done:
	case <-time.After(h.e.settleTimeout()):
		h.e.log.Warn("mods: turn.step chain did not settle after the request finished")
	}
}

// finish resolves core with a TurnStepResult; nil step is "no response"
// (the request was interrupted). Worker-only.
func (h *stepHandle) finish(step *TurnStep) {
	if !h.reached || h.finished || h.resolve == nil {
		return
	}
	h.finished = true
	if h.stopCtx != nil {
		h.stopCtx()
	}
	res := map[string]any{
		"turnId":     h.start.TurnID,
		"index":      h.start.Index,
		"answer":     "",
		"toolUses":   []any{},
		"stopReason": nil,
		"usage":      nil,
	}
	if step != nil {
		res["answer"] = step.Answer
		uses := []any{}
		for _, u := range step.ToolUses {
			var input any
			if len(u.Input) > 0 && json.Unmarshal(u.Input, &input) != nil {
				input = string(u.Input)
			}
			uses = append(uses, map[string]any{"name": u.Name, "input": input})
		}
		res["toolUses"] = uses
		if step.StopReason != "" {
			res["stopReason"] = step.StopReason
		}
		if u := turnUsage(step.Usage, step.Model); u != nil {
			res["usage"] = u
		}
	}
	v, err := h.e.toJS(res)
	if err != nil {
		v = goja.Undefined()
	}
	_ = h.resolve(v)
}

// BeginStep raises turn.step ({ turnId, index, model, effort?,
// messageCount, agentId? }) before a model request and returns once every
// hook's code before its yield* next(e) has run, that is when the chain
// reached core. kiln then makes the request and calls Finish with what came
// back. Chunks are not streamed through mods in v1: next(e) yields nothing
// and returns the whole result. If ctx ends before Finish, core resolves as
// a request with no response (stopReason and usage null).
func (e *Engine) BeginStep(ctx context.Context, start StepStart) StepHandle {
	h := &stepHandle{e: e, settled: make(chan struct{}), start: start}
	if e.closed.Load() || e.init != nil {
		close(h.settled)
		return h
	}
	ready := make(chan struct{})
	var once sync.Once
	signal := func() { once.Do(func() { close(ready) }) }
	posted := e.w.post(func() {
		vm := e.w.vm
		ev := map[string]any{
			"turnId":       start.TurnID,
			"index":        start.Index,
			"model":        start.Model,
			"messageCount": start.MessageCount,
		}
		if start.Effort != "" {
			ev["effort"] = start.Effort
		}
		if start.AgentID != "" {
			ev["agentId"] = start.AgentID
		}
		input, err := e.toJS(ev)
		if err != nil {
			close(h.settled)
			signal()
			return
		}
		c := e.newChain("turn.step", func(goja.Value) goja.Value {
			if h.reached {
				return h.core
			}
			h.reached = true
			p, resolve, _ := vm.NewPromise()
			h.resolve = resolve
			h.core = vm.ToValue(p)
			h.stopCtx = context.AfterFunc(ctx, func() {
				e.w.post(func() { h.finish(nil) })
			})
			signal()
			return h.core
		})
		e.runChain(c, input, func(_ goja.Value, err error) {
			if err != nil {
				e.log.Warn("mods: turn.step chain rejected", "err", err)
			}
			close(h.settled)
			signal()
		})
	})
	if !posted {
		close(h.settled)
		return h
	}
	select {
	case <-ready:
	case <-ctx.Done():
	case <-e.w.done:
	}
	return h
}

// --- drawing ---------------------------------------------------------------------

// Render raises ui.render for one site and returns one Drawing per mod with
// a hook on it, in load order: the tree its hook returned, or nil when it
// called next (or failed). The event is { surface: "terminal", component,
// requestId, props } with the props claude-code.d.ts declares for Pane
// ({ title, isFocused, bodyColumns, placement: "dock", scroll: { offset: 0,
// bodyRows }, view }) and AbovePrompt ({ hasSurvey: false, isWorking,
// maxRows, bodyColumns, scroll, view }). Function props in a tree become
// Callbacks, valid until the same mod's next drawing of the same site.
func (e *Engine) Render(ctx context.Context, site Site) []Drawing {
	got, err := await(ctx, e, func(send func([]Drawing)) {
		input, err := e.toJS(renderEvent(site))
		if err != nil {
			send(nil)
			return
		}
		var plugins []*plugin
		perPlugin := map[*plugin][]*hook{}
		for _, h := range e.hooks["ui.render"] {
			if _, seen := perPlugin[h.plugin]; !seen && e.matches(h, input) {
				plugins = append(plugins, h.plugin)
			}
			if e.matches(h, input) {
				perPlugin[h.plugin] = append(perPlugin[h.plugin], h)
			}
		}
		if len(plugins) == 0 {
			send(nil)
			return
		}
		out := make([]Drawing, len(plugins))
		left := len(plugins)
		for i, p := range plugins {
			i, p := i, p
			c := &chain{e: e, event: "ui.render", hooks: perPlugin[p], core: func(goja.Value) goja.Value { return goja.Undefined() }}
			e.runChain(c, input, func(v goja.Value, err error) {
				siteKey := p.name + "\x00" + site.Component + "\x00" + site.RequestID
				e.dropCallbacks(siteKey)
				out[i] = Drawing{Plugin: p.name}
				if err != nil {
					e.log.Warn(fmt.Sprintf("%s: ui.render failed: %v", p.name, err), "plugin", p.name)
				} else {
					d := &decoder{vm: e.w.vm, elementSym: e.js.elementSym, textLeft: MaxTreeText,
						addCallback: func(fn goja.Callable) Callback { return e.addCallback(siteKey, p, fn) }}
					tree, derr := d.decodeTree(v)
					if derr != nil {
						e.dropCallbacks(siteKey)
						e.log.Warn(fmt.Sprintf("%s: ui.render drew nothing: %v", p.name, derr), "plugin", p.name)
					} else {
						out[i].Tree = tree
					}
				}
				left--
				if left == 0 {
					send(out)
				}
			})
		}
	})
	if err != nil {
		return nil
	}
	return got
}

func renderEvent(site Site) map[string]any {
	view := map[string]any{}
	if site.ViewAgentID != "" {
		view["agentId"] = site.ViewAgentID
	}
	scroll := map[string]any{"offset": 0, "bodyRows": site.BodyRows}
	var props map[string]any
	switch site.Component {
	case "AbovePrompt":
		props = map[string]any{
			"hasSurvey": false, "isWorking": site.IsWorking, "maxRows": site.BodyRows,
			"bodyColumns": site.BodyColumns, "scroll": scroll, "view": view,
		}
	default:
		props = map[string]any{
			"title": site.Title, "isFocused": site.IsFocused, "bodyColumns": site.BodyColumns,
			"placement": "dock", "scroll": scroll, "view": view,
		}
	}
	surface := site.Surface
	if surface == "" {
		surface = "terminal"
	}
	return map[string]any{
		"surface":   surface,
		"component": site.Component,
		"requestId": site.RequestID,
		"props":     props,
	}
}

func (e *Engine) addCallback(siteKey string, p *plugin, fn goja.Callable) Callback {
	e.nextCallback++
	id := e.nextCallback
	e.callbacks[id] = callbackEntry{plugin: p, fn: fn}
	e.siteCallbacks[siteKey] = append(e.siteCallbacks[siteKey], id)
	return Callback{ID: id}
}

func (e *Engine) dropCallbacks(siteKey string) {
	for _, id := range e.siteCallbacks[siteKey] {
		delete(e.callbacks, id)
	}
	delete(e.siteCallbacks, siteKey)
}

// Press runs a Button's onPress (or any function prop) from the latest
// drawing it came from. It does not wait for the function; a callback from
// a drawing since replaced does nothing.
func (e *Engine) Press(cb Callback) {
	if e.closed.Load() {
		return
	}
	e.w.post(func() {
		entry, ok := e.callbacks[cb.ID]
		if !ok {
			e.log.Debug("mods: press of a callback no drawing holds", "id", cb.ID)
			return
		}
		ret, err := entry.fn(goja.Undefined())
		if err != nil {
			e.log.Warn(fmt.Sprintf("%s: onPress threw %v", entry.plugin.name, jsErr(err)), "plugin", entry.plugin.name)
			return
		}
		e.then(ret, func(goja.Value) {}, func(r goja.Value) {
			e.log.Warn(fmt.Sprintf("%s: onPress failed: %s", entry.plugin.name, reasonText(r)), "plugin", entry.plugin.name)
		})
	})
}
