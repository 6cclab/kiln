package mods

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSessionMessagesMainApiAndAgent(t *testing.T) {
	var gotAgent string
	var gotAPI bool
	h := newHarness(t, Options{Services: Services{
		Messages: func(_ context.Context, agent string, api bool) ([]any, error) {
			gotAgent, gotAPI = agent, api
			if agent != "" {
				return nil, errors.New("no transcript for " + agent)
			}
			if api {
				return []any{map[string]any{"role": "assistant", "content": []any{}}, map[string]any{"role": "user", "content": []any{}}}, nil
			}
			return []any{map[string]any{"role": "user", "text": "hi", "toolUses": []any{}}}, nil
		},
	}})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))

	rows, _ := call(t, h, "session.messages")["ok"].([]any)
	if len(rows) != 1 || gotAgent != "" || gotAPI {
		t.Errorf("messages() = %v (agent %q, api %v), want the one main row\n%s", rows, gotAgent, gotAPI, h.logs)
	}
	// The API form opens on a user message.
	rows, _ = call(t, h, "session.messages", map[string]any{"as": "api"})["ok"].([]any)
	if len(rows) != 1 || !gotAPI || rows[0].(map[string]any)["role"] != "user" {
		t.Errorf("messages({as:api}) = %v, want it to open on the user message", rows)
	}
	// An agent the session cannot read is a deny, never the main conversation.
	got, _ := call(t, h, "session.messages", map[string]any{"agentId": "a1"})["ok"].(map[string]any)
	if gotAgent != "a1" || !strings.Contains(fmt.Sprint(got["deny"]), "a1") {
		t.Errorf("messages({agentId}) = %v, want { deny } naming a1", got)
	}
	if out := call(t, h, "session.messages", map[string]any{"as": "xml"}); out["err"] == nil {
		t.Errorf("an unknown `as` did not reject: %v", out)
	}
}

func TestSessionMessagesKeepsTheNewest(t *testing.T) {
	rows := make([]any, 0, maxSessionMessages+5)
	for i := 0; i < maxSessionMessages+5; i++ {
		rows = append(rows, map[string]any{"role": "assistant", "text": fmt.Sprint(i)})
	}
	if got := capMessages(rows, false); len(got) != maxSessionMessages || got[len(got)-1].(map[string]any)["text"] != fmt.Sprint(maxSessionMessages+4) {
		t.Errorf("capMessages kept %d rows, want the newest %d", len(got), maxSessionMessages)
	}
	if got := capMessages(rows, true); len(got) != 0 {
		t.Errorf("capMessages(api) of rows with no user message = %d rows, want none (it must open on a user message)", len(got))
	}
}

func TestSessionIdentityAuthorizeAndSurfaces(t *testing.T) {
	h := newHarness(t, Options{Services: Services{Surfaces: func() []string { return []string{"terminal"} }}})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))

	id, _ := call(t, h, "session.identity")["ok"].(map[string]any)
	if id["deny"] == nil {
		t.Errorf("identity = %v, want { deny }: kiln installs no mod from managed settings", id)
	}
	// authorize never lends a credential: null, the d.ts's "no first-party credential".
	if out := call(t, h, "session.authorize"); out["ok"] != nil || out["err"] != nil {
		t.Errorf("authorize = %v, want null", out)
	}
	if got := call(t, h, "session.surfaces")["ok"]; !reflect.DeepEqual(got, []any{"terminal"}) {
		t.Errorf("surfaces = %v, want [terminal]", got)
	}
	if got := call(t, h, "session.surface")["ok"]; got != "terminal" {
		t.Errorf("surface = %v, want terminal", got)
	}
}

func TestSessionSurfacesEmptyWithoutATerminal(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	if got := call(t, h, "session.surfaces")["ok"]; !reflect.DeepEqual(got, []any{}) {
		t.Errorf("surfaces with no terminal = %#v, want []", got)
	}
	if out := call(t, h, "session.surface"); out["ok"] != nil || out["err"] != nil {
		t.Errorf("surface with no terminal = %v, want null", out)
	}
}

func TestSessionSendRaisesEventAndKilnDeliversNothing(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	res, _ := call(t, h, "session.send", map[string]any{"to": map[string]any{"sessionId": "s9"}, "text": "ok"})["ok"].(map[string]any)
	if res["isDelivered"] != false || !strings.Contains(fmt.Sprint(res["reason"]), "s9") {
		t.Errorf("send = %v, want not delivered, naming s9\n%s", res, h.logs)
	}
	seen := seenOf(t, h, "session.send")
	if len(seen) != 1 {
		t.Fatalf("recorder saw %d session.send, want 1\n%s", len(seen), h.logs)
	}
	want := map[string]any{"to": "s9", "text": "ok", "origin": map[string]any{"kind": "plugin", "name": "recorder"}}
	if !reflect.DeepEqual(seen[0]["e"], want) {
		t.Errorf("hook saw %v, want %v", seen[0]["e"], want)
	}
	if out := call(t, h, "session.send", map[string]any{"text": "no recipient"}); out["err"] == nil {
		t.Errorf("a send with no recipient did not reject: %v", out)
	}
}

func TestSessionSendHookRewritesAndRefuses(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "sessioner", "recorder")
	h.e.SessionStart(ctxT(t))
	call(t, h, "session.send", map[string]any{"to": "peer", "text": "quiet"})
	var texts []string
	for _, s := range seenOf(t, h, "session.send") {
		texts = append(texts, s["e"].(map[string]any)["text"].(string))
	}
	// Whichever of the two hooks is outer, the one inside sees the other's
	// rewrite or the original; the chain ran both once.
	if len(texts) != 1 || (texts[0] != "quiet" && texts[0] != "QUIET") {
		t.Fatalf("recorder saw %v\n%s", texts, h.logs)
	}
	if texts[0] != "QUIET" {
		t.Errorf("recorder (loaded after sessioner) saw %q, want sessioner's rewrite QUIET", texts[0])
	}
	res, _ := call(t, h, "session.send", map[string]any{"to": "blocked", "text": "x"})["ok"].(map[string]any)
	if res["isDelivered"] != false || res["reason"] != "sessioner blocks this recipient" {
		t.Errorf("a hook's refusal = %v, want its reason", res)
	}
}

func TestSessionReceiveRewritesAndConsumes(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "sessioner")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	origin := map[string]any{"kind": "task-notification"}
	if text, consumed := h.e.SessionReceive(ctx, origin, "build done"); text != "build done [seen]" || consumed != "" {
		t.Errorf("SessionReceive = %q, %q, want the hook's rewrite", text, consumed)
	}
	if text, consumed := h.e.SessionReceive(ctx, origin, "please mute this"); consumed != "muted by sessioner" || text != "" {
		t.Errorf("SessionReceive = %q, %q, want it consumed", text, consumed)
	}
}

func TestSessionReceivePassesThroughWithoutHooksAndRecordsOrigin(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "describer")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	if text, consumed := h.e.SessionReceive(ctx, map[string]any{"kind": "task-notification"}, "hi"); text != "hi" || consumed != "" {
		t.Errorf("SessionReceive with no hook = %q, %q, want the text back", text, consumed)
	}
	r := newHarness(t, Options{})
	r.load(t, "recorder")
	r.e.SessionStart(ctx)
	r.e.SessionReceive(ctx, map[string]any{"kind": "task-notification"}, "hi")
	seen := seenOf(t, r, "session.receive")
	want := map[string]any{"origin": map[string]any{"kind": "task-notification"}, "text": "hi"}
	if len(seen) != 1 || !reflect.DeepEqual(seen[0]["e"], want) {
		t.Errorf("recorder saw %v, want %v", seen, want)
	}
}

func TestSessionMeasureRaisesWhenTheFillMoves(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	h.e.SessionMeasure(ctx, Usage{ContextWindow: 1000, ContextTokens: 250})
	h.e.SessionMeasure(ctx, Usage{ContextWindow: 1000, ContextTokens: 250}) // unchanged: no event
	h.e.SessionMeasure(ctx, Usage{ContextWindow: 1000, ContextTokens: 500})
	seen := seenOf(t, h, "session.measure")
	if len(seen) != 2 {
		t.Fatalf("session.measure raised %d times, want 2 (the repeat moved nothing)\n%s", len(seen), h.logs)
	}
	want := map[string]any{
		"context":    map[string]any{"window": float64(1000), "tokens": float64(500), "percent": float64(50)},
		"rateLimits": []any{},
		"changed":    []any{"context"},
	}
	if !reflect.DeepEqual(seen[1]["e"], want) {
		t.Errorf("second measure = %v, want %v", seen[1]["e"], want)
	}
}

// A hook on an event kiln raises registers silently; one on an event it never
// raises (attach, detach: no remote client) says so in the log.
func TestSessionEventsLogWhatKilnDoesNotRaise(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder", "unknown")
	logs := h.logs.String()
	for _, ev := range []string{"session.send", "session.receive", "session.measure"} {
		if strings.Contains(logs, "kiln does not raise "+ev) {
			t.Errorf("kiln raises %s but logged that it does not:\n%s", ev, logs)
		}
	}
	if !strings.Contains(logs, "kiln does not raise session.attach") {
		t.Errorf("a session.attach hook was not reported as never raised:\n%s", logs)
	}
	if supportedEvents["session.detach"] {
		t.Error("session.detach is listed as raised: kiln has no remote client")
	}
}
