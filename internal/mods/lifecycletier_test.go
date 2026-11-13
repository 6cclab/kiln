package mods

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func call(t *testing.T, h *harness, fn string, args ...any) map[string]any {
	t.Helper()
	a, _ := json.Marshal(map[string]any{"fn": fn, "args": args})
	var out map[string]any
	if err := json.Unmarshal([]byte(runCommand(t, h, "call", string(a))), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func seenOf(t *testing.T, h *harness, event string) []map[string]any {
	t.Helper()
	var all []map[string]any
	if err := json.Unmarshal([]byte(runCommand(t, h, "dump", "")), &all); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, s := range all {
		if s["event"] == event {
			out = append(out, s)
		}
	}
	return out
}

func TestSessionEndPayload(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	h.e.SessionEnd("prompt_input_exit", "s1", "")
	got := seenOf(t, h, "session.end")
	if len(got) != 1 {
		t.Fatalf("session.end raised %d times, want 1\n%s", len(got), h.logs)
	}
	want := map[string]any{"reason": "prompt_input_exit", "sessionId": "s1", "resume": map[string]any{"id": ""}}
	if !reflect.DeepEqual(got[0]["e"], want) {
		t.Errorf("session.end e = %v, want %v", got[0]["e"], want)
	}
}

func TestSessionCompactCallRunsServiceAndMarksRaised(t *testing.T) {
	var gotInstr string
	h := newHarness(t, Options{Services: Services{
		Messages: func(context.Context, string, bool) ([]any, error) { return []any{}, nil },
		Compact: func(ctx context.Context, instructions string) (CompactResult, error) {
			gotInstr = instructions
			if !IsRaised(ctx, "session.compact") {
				t.Error("the $ call did not mark session.compact raised: kiln's own hook would raise it twice")
			}
			return CompactResult{Messages: []any{map[string]any{"role": "user", "text": "sum"}}, TokensBefore: 90, TokensAfter: 10}, nil
		},
	}})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	out := call(t, h, "session.compact", map[string]any{"instructions": "keep x"})
	res, _ := out["ok"].(map[string]any)
	if gotInstr != "keep x" || res["tokensBefore"] != float64(90) || res["tokensAfter"] != float64(10) {
		t.Errorf("compact: instructions %q, result %v\n%s", gotInstr, out, h.logs)
	}
	// The event ran through the plugin's session.compact hook (a hook other
	// than the calling command.run one), once, with trigger plugin.
	got := seenOf(t, h, "session.compact")
	if len(got) != 1 || got[0]["e"].(map[string]any)["trigger"] != "plugin" {
		t.Errorf("session.compact hooks saw %v, want one raise with trigger plugin", got)
	}
}

func TestSessionCompactDecisionRaisesOnce(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	d := h.e.SessionCompact(ctx, "", "auto", "ins", []any{map[string]any{"role": "user"}})
	if d.Skip != "" || d.Rewritten {
		t.Errorf("decision = %+v, want go on unchanged", d)
	}
	got := seenOf(t, h, "session.compact")
	if len(got) != 1 {
		t.Fatalf("recorder saw %d session.compact, want 1\n%s", len(got), h.logs)
	}
	e := got[0]["e"].(map[string]any)
	if e["trigger"] != "auto" || e["instructions"] != "ins" {
		t.Errorf("payload = %v", e)
	}
	h.e.SessionCompact(MarkRaised(ctx, "session.compact"), "", "manual", "", nil)
	if n := len(seenOf(t, h, "session.compact")); n != 1 {
		t.Errorf("a marked ctx raised again (%d events)", n)
	}
}

func TestSessionAppendCallQueuesUserRow(t *testing.T) {
	var gotText string
	h := newHarness(t, Options{Services: Services{
		Append: func(_ context.Context, _, text string) (string, error) {
			gotText = text
			return "u-1", nil
		},
	}})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	out := call(t, h, "session.append", map[string]any{"message": map[string]any{"type": "user", "content": "hello"}})
	res, _ := out["ok"].(map[string]any)
	if gotText != "hello" || res["uuid"] != "u-1" {
		t.Errorf("append: text %q, result %v\n%s", gotText, out, h.logs)
	}
	out = call(t, h, "session.append", map[string]any{"message": map[string]any{"type": "system", "content": "x"}})
	res, _ = out["ok"].(map[string]any)
	if _, deny := res["deny"]; !deny {
		t.Errorf("a system row was not denied: %v", out)
	}
}

func TestSessionAppendRowRaisesEvent(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	h.e.SessionAppend(AppendRow{Type: "user", Role: "user", Door: "prompt", UUID: "u9",
		Content: []any{map[string]any{"type": "text", "text": "hi"}}, Origin: map[string]any{"kind": "user"}})
	got := seenOf(t, h, "session.append")
	if len(got) != 1 {
		t.Fatalf("session.append raised %d times\n%s", len(got), h.logs)
	}
	e := got[0]["e"].(map[string]any)
	if e["door"] != "prompt" || e["uuid"] != "u9" {
		t.Errorf("payload = %v", e)
	}
}

func TestTurnStepStreamsChunksInOrder(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	step := h.e.BeginStep(ctx, StepStart{TurnID: "t1", Model: "m"})
	step.Chunk(StepChunk{"kind": "text", "index": 0, "text": "he"})
	step.Chunk(StepChunk{"kind": "text", "index": 0, "text": "llo"})
	step.Finish(TurnStep{TurnID: "t1", Model: "m", Answer: "hello", StopReason: "end_turn"})
	got := seenOf(t, h, "turn.step")
	if len(got) != 1 {
		t.Fatalf("turn.step recorded %d times\n%s", len(got), h.logs)
	}
	var texts []string
	for _, c := range got[0]["chunks"].([]any) {
		texts = append(texts, c.(map[string]any)["text"].(string))
	}
	if strings.Join(texts, "|") != "he|llo" || got[0]["answer"] != "hello" {
		t.Errorf("hook saw chunks %v then answer %v, want he|llo then hello", texts, got[0]["answer"])
	}
}

func TestTurnAbortCall(t *testing.T) {
	var aborted string
	h := newHarness(t, Options{Services: Services{
		AbortTurn: func(_ context.Context, id string) error {
			aborted = id
			if id == "bad" {
				return errors.New("not the running turn")
			}
			return nil
		},
	}})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	if out := call(t, h, "turn.abort", map[string]any{"turnId": "t7"}); out["err"] != nil || aborted != "t7" {
		t.Errorf("turn.abort t7 = %v, aborted %q\n%s", out, aborted, h.logs)
	}
	if out := call(t, h, "turn.abort", map[string]any{"turnId": "bad"}); out["err"] == nil {
		t.Errorf("an AbortTurn error did not reject: %v", out)
	}
}
