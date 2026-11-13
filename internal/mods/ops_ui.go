package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/dop251/goja"
)

// $.ui.ask/blit/copy/focus/log/notice/notify/scroll/selection/status, and the
// ui.scroll and ui.focus events the person's own keys raise on a pane.

// uiTextCap is how much of a status, log or notice line the terminal draws
// (Claude Code: the first 2000 characters).
const uiTextCap = 2000

// UIServices are what kiln's screen lends the ui.* methods. Each is optional:
// with none (a -p run, a test) the call resolves with the answer the d.ts
// gives for "no surface" and the hooks still run.
type UIServices struct {
	// Status pins text as the plugin's one line under the prompt ("" clears).
	Status func(plugin, text string)
	// Log appends a dim row to the transcript.
	Log func(plugin, text string)
	// Notice puts a line under the open permission dialog of toolUseID ("" text
	// removes it); a non-empty return refuses the call, saying why.
	Notice func(plugin, toolUseID, text string) string
	// Notify writes a native notification and says where it went.
	Notify func(ctx context.Context, title, text string) NotifyResult
	// Copy puts text on the clipboard; a non-empty return is the d.ts reason it
	// did not take ("no-clipboard", "refused").
	Copy func(ctx context.Context, text string) string
	// Selection is what the person last selected with the mouse.
	Selection func() (Selection, bool)
	// Site describes a pane the plugin owns: by requestID, or (""), the one that
	// draws key.
	Site func(plugin, requestID, key string) (SiteState, bool)
	// Scroll moves a pane's window; Focus puts its focus ring on a keyed
	// Button (a non-empty return refuses, saying why).
	Scroll func(requestID string, offset int)
	Focus  func(requestID, key string) string
	// Blit repaints a keyed Raster the plugin drew in a pane (a non-empty return
	// refuses, saying why).
	Blit func(plugin, requestID, key, cells string) string
}

// NotifyResult is where a notification went: Channel, or why it did not.
type NotifyResult struct {
	Channel string
	Reason  string
}

// Selection is the person's last mouse selection.
type Selection struct {
	Text      string
	RequestID string
}

// SiteState is a pane as the ui.scroll and ui.focus calls see it.
type SiteState struct {
	Component, RequestID        string
	Offset, BodyRows, Rows      int
	KeyRow                      int // the row of the key asked for; -1 when it is not drawn
	Focused, HasKey, TakesFocus bool
}

var askAnswer = regexp.MustCompile(`^User has answered your questions: "(?:[^"\\]|\\.)*"="((?:[^"\\]|\\.)*)"$`)

func capText(s string) string {
	r := []rune(s)
	if len(r) > uiTextCap {
		return string(r[:uiTextCap])
	}
	return s
}

func (e *Engine) answerUI(f *frame, name string, arg func(int) goja.Value) (out any, handled bool, err error) {
	ui := e.opts.Services.UI
	plugin := f.plugin.name
	noop := func(map[string]any) (any, error) { return nil, nil }
	denyOf := func(res map[string]any) (any, error) {
		if s := strOf(res, "deny"); s != "" {
			return map[string]any{"deny": s}, nil
		}
		return map[string]any{}, nil
	}
	call := func(event string, ev map[string]any, core OpResultFunc, shape func(map[string]any) (any, error)) goja.Value {
		return e.opCall(f, event,
			func(context.Context) (map[string]any, error) { return ev, nil },
			func(ctx context.Context) OpCore {
				return func(in map[string]any, done func(map[string]any, error)) { core(ctx, in, done) }
			}, shape)
	}
	switch name {
	case "ui.status":
		text := ""
		if v := arg(0); !goja.IsUndefined(v) && !goja.IsNull(v) {
			text = capText(str(v))
		}
		ev := map[string]any{"text": nil}
		if text != "" {
			ev["text"] = text
		}
		return call("ui.status", ev, func(_ context.Context, in map[string]any, done func(map[string]any, error)) {
			if ui.Status != nil {
				ui.Status(plugin, strOf(in, "text"))
			}
			done(map[string]any{}, nil)
		}, noop), true, nil

	case "ui.log":
		to := "transcript"
		if o := exportMap(arg(1)); o != nil && strOf(o, "to") != "" {
			to = strOf(o, "to")
		}
		if to != "transcript" && to != "debug" {
			return nil, true, fmt.Errorf(`to is "transcript" or "debug", not %q`, to)
		}
		ev := map[string]any{"text": str(arg(0)), "to": to}
		return call("ui.log", ev, func(_ context.Context, in map[string]any, done func(map[string]any, error)) {
			text := strOf(in, "text")
			if strOf(in, "to") == "debug" || ui.Log == nil {
				r := []rune(text)
				if len(r) > 10000 {
					text = string(r[:10000])
				}
				e.log.Debug("mods: ui.log", "plugin", plugin, "text", text)
			} else {
				ui.Log(plugin, capText(text))
			}
			done(map[string]any{}, nil)
		}, noop), true, nil

	case "ui.notice":
		id := str(arg(0))
		if id == "" {
			return nil, true, fmt.Errorf("takes (tool_use_id, text)")
		}
		ev := map[string]any{"tool_use_id": id, "text": nil}
		if v := arg(1); !goja.IsUndefined(v) && !goja.IsNull(v) {
			ev["text"] = capText(str(v))
		}
		return call("ui.notice", ev, func(_ context.Context, in map[string]any, done func(map[string]any, error)) {
			if ui.Notice == nil {
				done(nil, fmt.Errorf("no dialog is open for %s: kiln is not showing its interface", strOf(in, "tool_use_id")))
				return
			}
			if why := ui.Notice(plugin, strOf(in, "tool_use_id"), strOf(in, "text")); why != "" {
				done(nil, fmt.Errorf("%s", why))
				return
			}
			done(map[string]any{}, nil)
		}, noop), true, nil

	case "ui.notify":
		text := str(arg(0))
		title := plugin
		if o := exportMap(arg(1)); o != nil && strOf(o, "title") != "" {
			title = strOf(o, "title")
		}
		ev := map[string]any{"text": text, "title": title}
		return call("ui.notify", ev, func(ctx context.Context, in map[string]any, done func(map[string]any, error)) {
			if ui.Notify == nil {
				done(map[string]any{"isSent": false, "reason": "no-surface"}, nil)
				return
			}
			r := ui.Notify(ctx, strOf(in, "title"), strOf(in, "text"))
			if r.Channel != "" {
				done(map[string]any{"isSent": true, "channel": r.Channel}, nil)
				return
			}
			done(map[string]any{"isSent": false, "reason": r.Reason}, nil)
		}, func(res map[string]any) (any, error) {
			if s := strOf(res, "deny"); s != "" {
				return nil, fmt.Errorf("%s", s)
			}
			if _, ok := res["isSent"]; !ok {
				return map[string]any{"isSent": false, "reason": "refused"}, nil
			}
			return res, nil
		}), true, nil

	case "ui.copy":
		args := exportMap(arg(0))
		if args == nil || args["text"] == nil {
			return nil, true, fmt.Errorf("takes { text, surface? }")
		}
		surface := strOf(args, "surface")
		if surface == "" && e.opts.Services.Surfaces != nil {
			if s := e.opts.Services.Surfaces(); len(s) > 0 {
				surface = s[0]
			}
		}
		ev := map[string]any{"text": strOf(args, "text"), "surface": surface}
		return call("ui.copy", ev, func(ctx context.Context, in map[string]any, done func(map[string]any, error)) {
			if strOf(in, "surface") != "terminal" || ui.Copy == nil {
				done(map[string]any{"isCopied": false, "reason": "no-surface"}, nil)
				return
			}
			if why := ui.Copy(ctx, strOf(in, "text")); why != "" {
				done(map[string]any{"isCopied": false, "reason": why}, nil)
				return
			}
			done(map[string]any{"isCopied": true}, nil)
		}, func(res map[string]any) (any, error) {
			if _, ok := res["isCopied"]; !ok {
				return map[string]any{"isCopied": false, "reason": "refused"}, nil
			}
			return res, nil
		}), true, nil

	case "ui.selection":
		return call("ui.selection", map[string]any{}, func(_ context.Context, _ map[string]any, done func(map[string]any, error)) {
			if ui.Selection != nil {
				if s, ok := ui.Selection(); ok && s.Text != "" {
					out := map[string]any{"text": s.Text}
					if s.RequestID != "" {
						out["requestId"] = s.RequestID
					}
					done(out, nil)
					return
				}
			}
			done(map[string]any{}, nil)
		}, func(res map[string]any) (any, error) {
			if strOf(res, "text") == "" {
				return nil, nil
			}
			return res, nil
		}), true, nil

	case "ui.blit":
		args := exportMap(arg(0))
		if args == nil || strOf(args, "requestId") == "" || strOf(args, "key") == "" {
			return nil, true, fmt.Errorf("takes { requestId, key, cells | source }")
		}
		ev := map[string]any{}
		for k, v := range args {
			ev[k] = v
		}
		return call("ui.blit", ev, func(_ context.Context, in map[string]any, done func(map[string]any, error)) {
			if _, isImage := in["source"]; isImage {
				done(map[string]any{"deny": "kiln draws an Image as its alt text, so there is no source to swap"}, nil)
				return
			}
			cells, ok := in["cells"].(string)
			if !ok {
				done(map[string]any{"deny": "cells is the Raster's cells string"}, nil)
				return
			}
			if ui.Blit == nil {
				done(map[string]any{"deny": "nothing is mounted: kiln is not showing its interface"}, nil)
				return
			}
			done(map[string]any{"deny": ui.Blit(plugin, strOf(in, "requestId"), strOf(in, "key"), cells)}, nil)
		}, denyOf), true, nil

	case "ui.scroll":
		return e.uiScroll(f, arg(0), call, denyOf), true, nil

	case "ui.focus":
		return e.uiFocus(f, arg(0), call, denyOf), true, nil

	case "ui.ask":
		return e.uiAsk(f, arg(0), arg(1))
	}
	return nil, false, nil
}

// OpResultFunc is a ui core written as a plain function.
type OpResultFunc func(ctx context.Context, in map[string]any, done func(map[string]any, error))

type uiCall func(event string, ev map[string]any, core OpResultFunc, shape func(map[string]any) (any, error)) goja.Value

// uiScroll is $.ui.scroll: it resolves the target to a pane's window and an
// offset, raises ui.scroll under the plugin's origin and moves the pane to the
// offset the chain left.
func (e *Engine) uiScroll(f *frame, a goja.Value, call uiCall, denyOf func(map[string]any) (any, error)) goja.Value {
	ui := e.opts.Services.UI
	args := exportMap(a)
	in := strOf(args, "in")
	block := strOf(args, "block")
	if block == "" {
		block = "nearest"
	}
	deny := func(why string) goja.Value { return e.promiseOf(e.mustJS(map[string]any{"deny": why})) }
	var to, key string
	switch t := args["to"].(type) {
	case string:
		to = t
	case map[string]any:
		key = strOf(t, "key")
		if key == "" && strOf(t, "requestId") != "" {
			return deny("a transcript row moves only while the call answers the person's own input, and kiln moves none: only a pane's body scrolls")
		}
	}
	if (to != "start" && to != "end" && key == "") || (to != "" && in == "") {
		return deny("takes { to: 'start' | 'end', in } or { to: { key }, in? }")
	}
	if ui.Site == nil {
		return deny("no pane is open: kiln is not showing its interface")
	}
	site, ok := ui.Site(f.plugin.name, in, key)
	if !ok {
		return deny("no pane of " + f.plugin.name + " is open there")
	}
	last := site.Rows - site.BodyRows
	if last < 0 {
		last = 0
	}
	target := site.Offset
	switch {
	case to == "start":
		target = 0
	case to == "end":
		target = last
	case !site.HasKey:
		return deny("no Button keyed " + key + " is drawn in " + site.RequestID + "; kiln locates Buttons by key")
	default:
		r, h := site.KeyRow, site.BodyRows
		switch block {
		case "start":
			target = r
		case "end":
			target = r - h + 1
		case "center":
			target = r - h/2
		default:
			if r < site.Offset {
				target = r
			} else if r >= site.Offset+h {
				target = r - h + 1
			}
		}
	}
	if target > last {
		target = last
	}
	if target < 0 {
		target = 0
	}
	ev := map[string]any{"component": site.Component, "requestId": site.RequestID, "offset": target,
		"by": target - site.Offset, "bodyRows": site.BodyRows, "contentRows": site.Rows,
		"origin": map[string]any{"kind": "plugin", "name": f.plugin.name}}
	return call("ui.scroll", ev, func(_ context.Context, in map[string]any, done func(map[string]any, error)) {
		if ui.Scroll != nil {
			off, _ := in["offset"].(float64)
			ui.Scroll(site.RequestID, int(off))
		}
		done(map[string]any{"moved": true, "offset": in["offset"]}, nil)
	}, func(res map[string]any) (any, error) {
		if s := strOf(res, "deny"); s != "" {
			return map[string]any{"deny": s}, nil
		}
		return map[string]any{}, nil
	})
}

// uiFocus is $.ui.focus: it puts the focus ring of a pane that holds the keys
// on a keyed Button, raising ui.focus under the plugin's origin.
func (e *Engine) uiFocus(f *frame, a goja.Value, call uiCall, denyOf func(map[string]any) (any, error)) goja.Value {
	ui := e.opts.Services.UI
	args := exportMap(a)
	id, key := strOf(args, "requestId"), strOf(args, "key")
	deny := func(why string) goja.Value { return e.promiseOf(e.mustJS(map[string]any{"deny": why})) }
	if id == "" || key == "" {
		return deny("takes { requestId, key }")
	}
	if ui.Site == nil {
		return deny("no pane is open: kiln is not showing its interface")
	}
	site, ok := ui.Site(f.plugin.name, id, key)
	switch {
	case !ok:
		return deny("no pane of " + f.plugin.name + " is open as " + id)
	case !site.Focused:
		return deny(id + " does not hold the keys")
	case !site.HasKey || !site.TakesFocus:
		return deny("no Button keyed " + key + " is drawn in " + id + ": kiln's focus ring walks Buttons")
	}
	ev := map[string]any{"component": site.Component, "requestId": site.RequestID, "plugin": f.plugin.name,
		"element": key, "origin": map[string]any{"kind": "plugin", "name": f.plugin.name}}
	return call("ui.focus", ev, func(_ context.Context, in map[string]any, done func(map[string]any, error)) {
		if ui.Focus != nil {
			if why := ui.Focus(site.RequestID, strOf(in, "element")); why != "" {
				done(map[string]any{"deny": why}, nil)
				return
			}
		}
		done(map[string]any{"moved": true, "element": in["element"]}, nil)
	}, denyOf)
}

// uiAsk is $.ui.ask: a tool.call of AskUserQuestion through every hook but the
// calling one, answered by kiln's own question dialog.
func (e *Engine) uiAsk(f *frame, q, opts goja.Value) (any, bool, error) {
	svc := e.opts.Services
	if svc.CallTool == nil {
		return nil, true, noService("ui.ask")
	}
	question := strings.TrimSpace(str(q))
	if question == "" {
		return nil, true, fmt.Errorf("takes (question, options)")
	}
	header, multi := "", false
	var labels []any
	if list, ok := opts.Export().([]any); ok {
		labels = list
	} else if o := exportMap(opts); o != nil {
		labels, _ = o["options"].([]any)
		header = strOf(o, "header")
		multi, _ = o["multiSelect"].(bool)
	}
	if len(labels) < 2 || len(labels) > 4 {
		return nil, true, fmt.Errorf("takes 2-4 option labels")
	}
	options := make([]any, 0, len(labels))
	for _, l := range labels {
		options = append(options, map[string]any{"label": fmt.Sprint(l), "description": fmt.Sprint(l)})
	}
	if header == "" {
		header = f.plugin.name
		if r := []rune(header); len(r) > 12 {
			header = string(r[:12])
		}
	}
	id := fmt.Sprintf("toolu_mod_%d", modToolSeq.Add(1))
	in := map[string]any{"tool": "AskUserQuestion", "tool_use_id": id, "questions": []any{
		map[string]any{"question": question, "header": header, "options": options, "multiSelect": multi}}}
	return e.opCall(f, "tool.call",
		func(context.Context) (map[string]any, error) { return in, nil },
		func(ctx context.Context) OpCore {
			return func(ev map[string]any, done func(map[string]any, error)) {
				raw, err := json.Marshal(map[string]any{"questions": ev["questions"]})
				if err != nil {
					done(nil, err)
					return
				}
				res, err := svc.CallTool(ctx, strOf(ev, "tool"), raw, strOf(ev, "tool_use_id"))
				switch {
				case err != nil:
					done(nil, err)
				case res.Deny != "":
					done(map[string]any{"deny": res.Deny}, nil)
				case res.IsError:
					done(map[string]any{"deny": res.Text}, nil)
				default:
					done(map[string]any{"result": res.Text, "text": res.Text}, nil)
				}
			}
		},
		func(res map[string]any) (any, error) {
			if s := strOf(res, "deny"); s != "" {
				return nil, fmt.Errorf("%s", s)
			}
			text := strOf(res, "text")
			if text == "" {
				text = strOf(res, "result")
			}
			m := askAnswer.FindStringSubmatch(text)
			if m == nil {
				return nil, fmt.Errorf("the question was not answered")
			}
			ans, err := strconv.Unquote(`"` + m[1] + `"`)
			if err != nil {
				return m[1], nil
			}
			return ans, nil
		}), true, nil
}

// PersonScroll raises ui.scroll for the person's own keys on a pane (origin is
// { kind: "person" }) and reports the offset the chain left; moved is false
// when a hook kept the window where it was.
func (e *Engine) PersonScroll(ctx context.Context, ev map[string]any) (offset int, moved bool) {
	ev["origin"] = map[string]any{"kind": "person"}
	res, err := e.Raise(ctx, "ui.scroll", ev, OpResult(func(in map[string]any) (map[string]any, error) {
		return map[string]any{"moved": true, "offset": in["offset"]}, nil
	}))
	if err != nil {
		e.log.Warn("mods: ui.scroll chain failed; the pane stays", "err", err)
		return 0, false
	}
	if ok, _ := res["moved"].(bool); !ok {
		return 0, false
	}
	off, _ := res["offset"].(float64)
	return int(off), true
}

// PersonFocus raises ui.focus for the person's Tab on a pane and reports the
// element the chain landed it on ("" leaves the engine's stop to kiln); moved
// is false when a hook kept the focus where it was.
func (e *Engine) PersonFocus(ctx context.Context, ev map[string]any) (element string, moved bool) {
	ev["origin"] = map[string]any{"kind": "person"}
	res, err := e.Raise(ctx, "ui.focus", ev, OpResult(func(in map[string]any) (map[string]any, error) {
		return map[string]any{"moved": true, "element": in["element"]}, nil
	}))
	if err != nil {
		e.log.Warn("mods: ui.focus chain failed; the focus stays", "err", err)
		return "", false
	}
	if ok, _ := res["moved"].(bool); !ok {
		return "", false
	}
	return strOf(res, "element"), true
}
