package mods

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type uiLog struct {
	status, logs, notices []string
	scrolled              []int
	focused               []string
	blits                 []string
}

func (u *uiLog) services() Services {
	return Services{
		Surfaces: func() []string { return []string{"terminal"} },
		UI: UIServices{
			Status: func(p, text string) { u.status = append(u.status, p+":"+text) },
			Log:    func(p, text string) { u.logs = append(u.logs, p+":"+text) },
			Notice: func(p, id, text string) string {
				if id != "open" {
					return "no dialog is open for " + id
				}
				u.notices = append(u.notices, text)
				return ""
			},
			Notify: func(_ context.Context, title, text string) NotifyResult {
				if text == "off" {
					return NotifyResult{Reason: "disabled"}
				}
				return NotifyResult{Channel: "terminal_bell"}
			},
			Copy: func(_ context.Context, text string) string {
				if text == "no" {
					return "refused"
				}
				return ""
			},
			Selection: func() (Selection, bool) { return Selection{Text: "picked"}, true },
			Site: func(plugin, id, key string) (SiteState, bool) {
				if id != "files" {
					return SiteState{}, false
				}
				s := SiteState{Component: "Pane", RequestID: "files", Offset: 2, BodyRows: 5, Rows: 40, KeyRow: -1, Focused: true}
				if key == "row:30" {
					s.HasKey, s.TakesFocus, s.KeyRow = true, true, 30
				}
				return s, true
			},
			Scroll: func(_ string, off int) { u.scrolled = append(u.scrolled, off) },
			Focus: func(_, key string) string {
				u.focused = append(u.focused, key)
				return ""
			},
			Blit: func(_, _, key, cells string) string {
				u.blits = append(u.blits, key+"="+cells)
				return ""
			},
		},
	}
}

func uiHarness(t *testing.T, u *uiLog) *harness {
	t.Helper()
	h := newHarness(t, Options{Services: u.services()})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	return h
}

func okOf(out map[string]any) map[string]any {
	m, _ := out["ok"].(map[string]any)
	return m
}

func TestUIStatusLogNoticeReachScreenThroughHooks(t *testing.T) {
	u := &uiLog{}
	h := uiHarness(t, u)
	call(t, h, "ui.status", "building")
	call(t, h, "ui.status")
	call(t, h, "ui.log", "a line")
	call(t, h, "ui.log", "quiet", map[string]any{"to": "debug"})
	if got := strings.Join(u.status, "|"); got != "recorder:building|recorder:" {
		t.Errorf("status = %q, want the text then a clear", got)
	}
	if len(u.logs) != 1 || u.logs[0] != "recorder:a line" {
		t.Errorf("transcript rows = %v, want only the transcript one (debug stays off screen)", u.logs)
	}
	if ev := seenOf(t, h, "ui.status"); len(ev) != 2 || ev[1]["e"].(map[string]any)["text"] != nil {
		t.Errorf("ui.status hooks saw %v, want two raises, the second with text null", ev)
	}
	if ev := seenOf(t, h, "ui.log"); len(ev) != 2 || ev[1]["e"].(map[string]any)["to"] != "debug" {
		t.Errorf("ui.log hooks saw %v, want to filled on each", ev)
	}
	if out := call(t, h, "ui.notice", "open", "checked"); out["err"] != nil || len(u.notices) != 1 {
		t.Errorf("notice on an open dialog = %v / %v", out, u.notices)
	}
	if out := call(t, h, "ui.notice", "gone", "x"); !strings.Contains(out["err"].(string), "no dialog is open for gone") {
		t.Errorf("notice on a call that is not open = %v, want a refusal", out)
	}
}

func TestUINotifyCopySelectionAnswerLikeClaudeCode(t *testing.T) {
	u := &uiLog{}
	h := uiHarness(t, u)
	if r := okOf(call(t, h, "ui.notify", "done", map[string]any{"title": "CI"})); r["isSent"] != true || r["channel"] != "terminal_bell" {
		t.Errorf("notify = %v", r)
	}
	if r := okOf(call(t, h, "ui.notify", "off")); r["isSent"] != false || r["reason"] != "disabled" {
		t.Errorf("notify with the channel off = %v", r)
	}
	if ev := seenOf(t, h, "ui.notify"); ev[0]["e"].(map[string]any)["title"] != "CI" || ev[1]["e"].(map[string]any)["title"] != "recorder" {
		t.Errorf("ui.notify hooks saw %v, want the title, defaulting to the plugin's name", ev)
	}
	if r := okOf(call(t, h, "ui.copy", map[string]any{"text": "x"})); r["isCopied"] != true {
		t.Errorf("copy = %v", r)
	}
	if r := okOf(call(t, h, "ui.copy", map[string]any{"text": "no"})); r["isCopied"] != false || r["reason"] != "refused" {
		t.Errorf("refused copy = %v", r)
	}
	if ev := seenOf(t, h, "ui.copy"); ev[0]["e"].(map[string]any)["surface"] != "terminal" {
		t.Errorf("ui.copy hooks saw %v, want surface filled with the session's first", ev)
	}
	if r := okOf(call(t, h, "ui.selection")); r["text"] != "picked" {
		t.Errorf("selection = %v", r)
	}
}

func TestUIWithoutScreenAnswersNoSurface(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	if r := okOf(call(t, h, "ui.notify", "x")); r["reason"] != "no-surface" {
		t.Errorf("notify in a -p run = %v", r)
	}
	if r := okOf(call(t, h, "ui.copy", map[string]any{"text": "x"})); r["reason"] != "no-surface" {
		t.Errorf("copy in a -p run = %v", r)
	}
	if out := call(t, h, "ui.selection"); out["ok"] != nil {
		t.Errorf("selection in a -p run = %v, want undefined", out)
	}
	if out := call(t, h, "ui.status", "x"); out["err"] != nil {
		t.Errorf("status in a -p run = %v, want a quiet no-op", out)
	}
}

func TestUIScrollRaisesEventAndMovesPane(t *testing.T) {
	u := &uiLog{}
	h := uiHarness(t, u)
	if r := okOf(call(t, h, "ui.scroll", map[string]any{"to": map[string]any{"key": "row:30"}, "in": "files", "block": "start"})); r["deny"] != nil {
		t.Fatalf("scroll denied: %v\n%s", r, h.logs)
	}
	if len(u.scrolled) != 1 || u.scrolled[0] != 30 {
		t.Errorf("pane moved to %v, want offset 30 (row 30 at the window's start)", u.scrolled)
	}
	ev := seenOf(t, h, "ui.scroll")
	if len(ev) != 1 {
		t.Fatalf("ui.scroll raised %d times, want 1", len(ev))
	}
	e := ev[0]["e"].(map[string]any)
	if e["by"] != float64(28) || e["bodyRows"] != float64(5) || e["contentRows"] != float64(40) || e["origin"].(map[string]any)["kind"] != "plugin" {
		t.Errorf("ui.scroll e = %v", e)
	}
	call(t, h, "ui.scroll", map[string]any{"to": "end", "in": "files"})
	if u.scrolled[1] != 35 {
		t.Errorf("end = %v, want the last window (35)", u.scrolled)
	}
	for name, args := range map[string]map[string]any{
		"missing site": {"to": "start", "in": "other"},
		"unknown key":  {"to": map[string]any{"key": "nope"}, "in": "files"},
		"row":          {"to": map[string]any{"requestId": "m1"}},
		"no in":        {"to": "end"},
	} {
		if r := okOf(call(t, h, "ui.scroll", args)); r["deny"] == nil {
			t.Errorf("%s: scroll = %v, want { deny }", name, r)
		}
	}
}

func TestUIFocusRaisesEventAndMovesRing(t *testing.T) {
	u := &uiLog{}
	h := uiHarness(t, u)
	if r := okOf(call(t, h, "ui.focus", map[string]any{"requestId": "files", "key": "row:30"})); r["deny"] != nil {
		t.Fatalf("focus denied: %v", r)
	}
	if len(u.focused) != 1 || u.focused[0] != "row:30" {
		t.Errorf("focus ring = %v", u.focused)
	}
	if ev := seenOf(t, h, "ui.focus"); len(ev) != 1 || ev[0]["e"].(map[string]any)["element"] != "row:30" {
		t.Errorf("ui.focus hooks saw %v", ev)
	}
	if r := okOf(call(t, h, "ui.focus", map[string]any{"requestId": "files", "key": "nope"})); r["deny"] == nil {
		t.Errorf("focus on an undrawn key = %v, want { deny }", r)
	}
}

func TestUIBlitDeniesImagesAndPatchesRasters(t *testing.T) {
	u := &uiLog{}
	h := uiHarness(t, u)
	if r := okOf(call(t, h, "ui.blit", map[string]any{"requestId": "files", "key": "r", "cells": "AQID"})); r["deny"] != "" && r["deny"] != nil {
		t.Errorf("blit = %v", r)
	}
	if len(u.blits) != 1 || u.blits[0] != "r=AQID" {
		t.Errorf("blits = %v", u.blits)
	}
	if r := okOf(call(t, h, "ui.blit", map[string]any{"requestId": "files", "key": "r", "source": map[string]any{}})); !strings.Contains(r["deny"].(string), "Image") {
		t.Errorf("image blit = %v, want kiln's honest refusal", r)
	}
}

func TestUIAskIsAnAskUserQuestionToolCall(t *testing.T) {
	var gotTool, gotArgs string
	u := &uiLog{}
	svc := u.services()
	svc.CallTool = func(_ context.Context, tool string, raw json.RawMessage, id string) (ToolOutput, error) {
		gotTool, gotArgs = tool, string(raw)
		return ToolOutput{Text: `User has answered your questions: "How careful?"="Care\"ful"`}, nil
	}
	h := newHarness(t, Options{Services: svc})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	out := call(t, h, "ui.ask", "How careful?", []any{"Bold", "Care\"ful"})
	if out["ok"] != `Care"ful` {
		t.Fatalf("ask = %v\n%s", out, h.logs)
	}
	if gotTool != "AskUserQuestion" || !strings.Contains(gotArgs, `"multiSelect":false`) || !strings.Contains(gotArgs, `"label":"Bold"`) {
		t.Errorf("tool %q args %s", gotTool, gotArgs)
	}
	if ev := seenOf(t, h, "tool.call"); len(ev) != 1 || ev[0]["e"].(map[string]any)["tool"] != "AskUserQuestion" {
		t.Errorf("tool.call hooks saw %v, want the ask as an AskUserQuestion call", ev)
	}
	if out := call(t, h, "ui.ask", "One?", []any{"a"}); out["err"] == nil {
		t.Errorf("one option = %v, want a refusal", out)
	}
}

func TestUIAskRejectsWhenDismissed(t *testing.T) {
	u := &uiLog{}
	svc := u.services()
	svc.CallTool = func(context.Context, string, json.RawMessage, string) (ToolOutput, error) {
		return ToolOutput{Text: "The user declined to answer.", IsError: true}, nil
	}
	h := newHarness(t, Options{Services: svc})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	out := call(t, h, "ui.ask", "Sure?", []any{"Yes", "No"})
	if s, _ := out["err"].(string); !strings.Contains(s, "declined") {
		t.Errorf("dismissed ask = %v, want a rejection", out)
	}
}

func TestPersonScrollAndFocusRunThroughHooks(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	off, moved := h.e.PersonScroll(ctx, map[string]any{"component": "Pane", "requestId": "files", "offset": 3, "by": 1, "bodyRows": 5, "contentRows": 40})
	if !moved || off != 3 {
		t.Errorf("scroll = %d %v, want the offset core was given", off, moved)
	}
	if ev := seenOf(t, h, "ui.scroll"); len(ev) != 1 || ev[0]["e"].(map[string]any)["origin"].(map[string]any)["kind"] != "person" {
		t.Errorf("ui.scroll hooks saw %v, want origin person", ev)
	}
	el, moved := h.e.PersonFocus(ctx, map[string]any{"component": "Pane", "requestId": "files", "plugin": "drawer", "element": "go"})
	if !moved || el != "go" {
		t.Errorf("focus = %q %v", el, moved)
	}
}

func TestUIPressRunsThroughHooks(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "drawer", "recorder")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	site := Site{Component: "Pane", RequestID: "drawer", Title: "T", BodyColumns: 40, BodyRows: 10}
	ds := h.e.Render(ctx, site)
	if len(ds) != 1 || ds[0].Tree == nil {
		t.Fatalf("Render = %+v\n%s", ds, h.logs)
	}
	h.e.Press(ds[0].Tree.Children[4].Props["onPress"].(Callback))
	if got := stateJSON(t, h.e, "drawer", "presses"); got != float64(1) {
		t.Fatalf("the press did not reach the element's own onPress: presses = %v\n%s", got, h.logs)
	}
	ev := seenOf(t, h, "ui.press")
	if len(ev) != 1 {
		t.Fatalf("ui.press raised %d times, want 1", len(ev))
	}
	e := ev[0]["e"].(map[string]any)
	if e["plugin"] != "drawer" || e["element"] != "go" || e["component"] != "Pane" || e["requestId"] != "drawer" || e["surface"] != "terminal" {
		t.Errorf("ui.press e = %v", e)
	}
}

func TestRenderOtherComponentsCarryTheirProps(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "rowdrawer")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	site := Site{Component: "CommandOutput", RequestID: "status", BodyColumns: 60,
		Props: map[string]any{"command": "status", "args": "", "text": "all good", "isErrored": false}}
	ds := h.e.Render(ctx, site)
	if len(ds) != 1 || ds[0].Tree == nil {
		t.Fatalf("Render = %+v\n%s", ds, h.logs)
	}
	if got := joined(ds[0].Tree); got != "mod row for status: all good (status)" {
		t.Errorf("row = %q", got)
	}
	site.Props = map[string]any{"command": "cost", "args": "", "text": "x", "isErrored": false}
	if ds := h.e.Render(ctx, site); len(ds) != 0 {
		t.Errorf("a hook matched on command status drew for cost: %+v", ds)
	}
}

func TestUIPressHookCanTakeThePress(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "drawer", "presstaker")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	ds := h.e.Render(ctx, Site{Component: "Pane", RequestID: "drawer", Title: "T", BodyColumns: 40, BodyRows: 10})
	h.e.Press(ds[0].Tree.Children[4].Props["onPress"].(Callback))
	h.e.Has("ui.press") // a round trip through the worker: the press has run by now
	if got := stateJSON(t, h.e, "drawer", "presses"); got != nil && got != float64(0) {
		t.Errorf("a hook took the press, yet onPress ran: presses = %v", got)
	}
}

// ui.input, ui.select, ui.message and ui.fault are about elements kiln does
// not draw (Input, Select, Client): a hook on them registers and kiln says it
// never raises them.
func TestUIEventsOfElementsKilnDoesNotDrawAreNeverRaised(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "clientless")
	logs := h.logs.String()
	for _, ev := range []string{"ui.input", "ui.select", "ui.message", "ui.fault"} {
		if supportedEvents[ev] {
			t.Errorf("%s is listed as raised, yet kiln draws no element that could raise it", ev)
		}
		if !strings.Contains(logs, "kiln does not raise "+ev) {
			t.Errorf("a %s hook was not reported as never raised:\n%s", ev, logs)
		}
	}
}
