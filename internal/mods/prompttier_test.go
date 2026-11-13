package mods

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestPromptSubmitRewritesDropsAndServesMedia(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder", "prompter")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)

	out := h.e.PromptSubmit(ctx, PromptSubmitIn{
		Text:        "fix the bug",
		Attachments: []PromptAttachment{{Type: "image", MediaType: "image/png", Data: []byte("abcd")}},
	})
	if out.Drop != "" || out.Text != "FIX THE BUG" || !reflect.DeepEqual(out.Context, []string{"ctx:media:image/png:4"}) {
		t.Errorf("PromptSubmit = %+v, want the upper-cased text and the context naming the image the hook read\n%s", out, h.logs)
	}
	seen := seenOf(t, h, "prompt.submit")
	if len(seen) != 1 || seen[0]["e"].(map[string]any)["origin"].(map[string]any)["kind"] != "composer" {
		t.Fatalf("recorder saw %v, want one submit with origin composer", seen)
	}
	id := seen[0]["e"].(map[string]any)["attachments"].([]any)[0].(map[string]any)["id"].(string)
	// Once the submission settled, its media is no longer readable.
	got := call(t, h, "prompt.media", map[string]any{"id": id})["ok"].(map[string]any)
	if got["deny"] == nil {
		t.Errorf("prompt.media after the submission settled = %v, want a deny", got)
	}

	out = h.e.PromptSubmit(ctx, PromptSubmitIn{Text: "forbidden thing"})
	if out.Drop != "no forbidden words" {
		t.Errorf("PromptSubmit(forbidden) = %+v, want a drop", out)
	}
	// A command's expansion is not rewritten, only dropped or given context.
	out = h.e.PromptSubmit(ctx, PromptSubmitIn{Text: "expanded", Command: &PromptCommand{Name: "review", Args: "x"}})
	if out.Text != "expanded" || len(out.Context) != 1 {
		t.Errorf("PromptSubmit(command) = %+v, want the text kept and the hook's context added", out)
	}
}

func TestPromptSubmitPassesThroughWithoutHooks(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "describer")
	h.e.SessionStart(ctxT(t))
	out := h.e.PromptSubmit(ctxT(t), PromptSubmitIn{Text: "hi", Context: []string{"c"}})
	if out.Text != "hi" || out.Drop != "" || !reflect.DeepEqual(out.Context, []string{"c"}) {
		t.Errorf("PromptSubmit with no hook = %+v, want the input back", out)
	}
}

func TestPromptSubmitCall(t *testing.T) {
	var gotText, gotPlugin string
	var gotAsUser bool
	h := newHarness(t, Options{Services: Services{
		PromptSubmit: func(_ context.Context, text string, _ []string, plugin string, asUser bool) error {
			gotText, gotPlugin, gotAsUser = text, plugin, asUser
			return nil
		},
	}})
	h.load(t, "recorder", "prompter")
	h.e.SessionStart(ctxT(t))
	res := call(t, h, "prompt.submit", map[string]any{"text": "hello", "asUser": true})["ok"].(map[string]any)
	if gotText != "HELLO" || gotPlugin != "recorder" || !gotAsUser || res["text"] != "HELLO" {
		t.Errorf("prompt.submit: service saw %q from %q asUser %v, result %v\n%s", gotText, gotPlugin, gotAsUser, res, h.logs)
	}
	seen := seenOf(t, h, "prompt.submit")
	if len(seen) != 1 {
		t.Fatalf("hooks saw %d submits, want 1", len(seen))
	}
	origin := seen[0]["e"].(map[string]any)["origin"].(map[string]any)
	if origin["kind"] != "plugin" || origin["name"] != "recorder" || origin["asUser"] != true {
		t.Errorf("origin = %v, want the calling plugin's", origin)
	}
	// A hook's drop is the call's answer.
	res = call(t, h, "prompt.submit", map[string]any{"text": "forbidden"})["ok"].(map[string]any)
	if res["drop"] != "no forbidden words" {
		t.Errorf("a dropped submit = %v", res)
	}
}

func TestPromptSubmitCallWithoutASession(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	res := call(t, h, "prompt.submit", map[string]any{"text": "hello"})["ok"].(map[string]any)
	if d, _ := res["drop"].(string); !strings.Contains(d, "-p") {
		t.Errorf("prompt.submit headless = %v, want a drop that says kiln -p has no idle session", res)
	}
}

func TestPromptReadFillSuggest(t *testing.T) {
	box := PromptBox{Text: "draft", Cursor: 2}
	var filled string
	h := newHarness(t, Options{Services: Services{
		PromptRead: func() (PromptBox, bool) { return box, true },
		PromptFill: func(_ context.Context, text, mode, plugin string) PromptFillAnswer {
			filled = mode + ":" + text + ":" + plugin
			box = PromptBox{Text: text, Cursor: len(text)}
			return PromptFillAnswer{Filled: true, Box: box}
		},
		PromptSuggest: func(_ context.Context, text string) bool { filled = "suggest:" + text; return true },
	}})
	h.load(t, "recorder", "prompter")
	h.e.SessionStart(ctxT(t))

	if got := call(t, h, "prompt.read")["ok"]; !reflect.DeepEqual(got, map[string]any{"text": "draft", "cursor": float64(2)}) {
		t.Errorf("prompt.read = %v", got)
	}
	res := call(t, h, "prompt.fill", map[string]any{"text": "  hi  ", "mode": "append"})["ok"].(map[string]any)
	if filled != "append:hi:recorder" || res["isFilled"] != true || res["text"] != "hi" || res["cursor"] != float64(2) {
		t.Errorf("prompt.fill: service saw %q, result %v (the hook trims the text)\n%s", filled, res, h.logs)
	}
	fills := seenOf(t, h, "prompt.fill")
	if len(fills) != 1 || fills[0]["e"].(map[string]any)["origin"].(map[string]any)["name"] != "recorder" {
		t.Errorf("fill hooks saw %v", fills)
	}
	res = call(t, h, "prompt.suggest", map[string]any{"text": " run it "})["ok"].(map[string]any)
	if filled != "suggest:run it" || res["isShown"] != true {
		t.Errorf("prompt.suggest: service saw %q, result %v", filled, res)
	}
	// A hook that keeps the text out never reaches the box.
	filled = ""
	res = call(t, h, "prompt.suggest", map[string]any{"text": "the secret"})["ok"].(map[string]any)
	if filled != "" || res["isShown"] != false {
		t.Errorf("a suppressed suggestion reached the box: %q %v", filled, res)
	}
	if res := call(t, h, "prompt.fill", map[string]any{"text": "x", "mode": "bogus"}); res["err"] == nil {
		t.Errorf("prompt.fill with a bad mode = %v, want an error", res)
	}
}

func TestPromptBoxHeadless(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	if got := call(t, h, "prompt.read")["ok"]; !reflect.DeepEqual(got, map[string]any{"text": "", "cursor": float64(0)}) {
		t.Errorf("prompt.read headless = %v, want the empty box", got)
	}
	res := call(t, h, "prompt.fill", map[string]any{"text": "x"})["ok"].(map[string]any)
	if res["isFilled"] != false || res["refusal"] != "no_composer" || res["text"] != "" {
		t.Errorf("prompt.fill headless = %v, want no_composer", res)
	}
	res = call(t, h, "prompt.suggest", map[string]any{"text": "x"})["ok"].(map[string]any)
	if res["isShown"] != false {
		t.Errorf("prompt.suggest headless = %v, want isShown false", res)
	}
}

func TestPromptSuggestEnginePath(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder", "prompter")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)
	if got := h.e.PromptSuggest(ctx, "  run tests "); got != "run tests" {
		t.Errorf("PromptSuggest = %q, want the hook's trimmed text", got)
	}
	if got := h.e.PromptSuggest(ctx, "the secret"); got != "" {
		t.Errorf("PromptSuggest(secret) = %q, want it dropped", got)
	}
	seen := seenOf(t, h, "prompt.suggest")
	if len(seen) != 2 || seen[0]["e"].(map[string]any)["origin"].(map[string]any)["kind"] != "suggestion" {
		t.Errorf("hooks saw %v, want two suggestions with origin kind suggestion", seen)
	}
}

func TestPromptEditChangeAutocompleteEvents(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder", "prompter")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)

	h.e.PromptEdit(ctx, PromptBox{Text: "héllo 😀x", Cursor: 9}, 8, 9, "yz")
	edits := seenOf(t, h, "prompt.edit")
	want := map[string]any{"origin": map[string]any{"kind": "composer"}, "text": "héllo 😀x", "cursor": float64(9),
		"start": float64(8), "end": float64(9), "inputText": "yz"}
	if len(edits) != 1 || !reflect.DeepEqual(edits[0]["e"], want) {
		t.Errorf("prompt.edit saw %v, want %v", edits, want)
	}

	h.e.PromptChange(ctx, PromptBox{Text: "ls", Cursor: 2}, map[string]any{"kind": "history"})
	changes := seenOf(t, h, "prompt.change")
	if len(changes) != 1 || changes[0]["e"].(map[string]any)["origin"].(map[string]any)["kind"] != "history" {
		t.Errorf("prompt.change saw %v", changes)
	}

	rows := h.e.PromptAutocomplete(ctx, PromptBox{Text: "see #1", Cursor: 6}, "#1", 4)
	if !reflect.DeepEqual(rows, []PromptSuggestion{{Text: "#123", Label: "issue 123", Description: "a bug"}}) {
		t.Errorf("PromptAutocomplete = %+v, want the hook's one row (the empty one is dropped)", rows)
	}
	if rows := h.e.PromptAutocomplete(ctx, PromptBox{Text: "see x", Cursor: 5}, "x", 4); rows != nil {
		t.Errorf("a token the hook's matcher refuses got rows: %+v", rows)
	}
	ac := seenOf(t, h, "prompt.autocomplete")
	if len(ac) != 2 {
		t.Errorf("autocomplete hooks saw %d events, want 2", len(ac))
	}
}

func TestPromptMentionAndAttachment(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder", "prompter")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)

	if r := h.e.PromptMention(ctx, ".env", "/w/.env"); r.Deny != "a secret" {
		t.Errorf("PromptMention(.env) = %+v, want the hook's deny", r)
	}
	r := h.e.PromptMention(ctx, "redirect.txt", "/w/redirect.txt")
	if r.Path != "/w/real.txt" || !reflect.DeepEqual(r.Context, []string{"mention-context"}) || r.Deny != "" || r.Skip {
		t.Errorf("PromptMention(redirect) = %+v, want the redirected path and context", r)
	}
	if r := h.e.PromptMention(ctx, "a.go", "/w/a.go"); r.Path != "/w/a.go" || r.Deny != "" || r.Skip || len(r.Context) != 0 {
		t.Errorf("PromptMention(a.go) = %+v, want it untouched", r)
	}
	if n := len(seenOf(t, h, "prompt.mention")); n != 3 {
		t.Errorf("mention hooks saw %d events, want 3", n)
	}

	if text, keep := h.e.PromptAttachment(ctx, "plan_mode", "plan", nil); text != "plan!" || !keep {
		t.Errorf("PromptAttachment(plan_mode) = %q %v", text, keep)
	}
	if _, keep := h.e.PromptAttachment(ctx, "drop_me", "x", map[string]any{"kind": "hook", "event": "SessionStart"}); keep {
		t.Error("an attachment a hook answered { text: null } for was kept")
	}
	if text, keep := h.e.PromptAttachment(ctx, "todo_reminder", "t", nil); text != "t" || !keep {
		t.Errorf("an attachment no hook matched = %q %v", text, keep)
	}
	att := seenOf(t, h, "prompt.attachment")
	if len(att) != 3 || att[0]["e"].(map[string]any)["origin"].(map[string]any)["kind"] != "engine" {
		t.Errorf("attachment hooks saw %v", att)
	}
}

func TestPromptSectionsAndCompose(t *testing.T) {
	facts := map[string]any{"model": "m1", "promptModel": "m1", "surfaces": []any{"terminal"}, "tools": []any{"Read"},
		"outputStyle": nil, "traits": []any{}}
	base := []PromptSection{
		{ID: "base", Text: "BASE", Scope: "shared"},
		{ID: "memory", Text: "MEM", Scope: "session"},
		{ID: "env", Text: "ENV", Scope: "session"},
	}
	h := newHarness(t, Options{Services: Services{
		PromptCompose: func(_ context.Context, args map[string]any) (map[string]any, []PromptSection) {
			f := map[string]any{}
			for k, v := range facts {
				f[k] = v
			}
			if m, ok := args["model"].(string); ok {
				f["model"] = m
			}
			return f, base
		},
	}})
	h.load(t, "recorder", "prompter")
	ctx := ctxT(t)
	h.e.SessionStart(ctx)

	res := call(t, h, "prompt.compose", map[string]any{"model": "m2"})["ok"].(map[string]any)
	var ids, texts []string
	for _, s := range res["sections"].([]any) {
		m := s.(map[string]any)
		ids = append(ids, m["id"].(string))
		texts = append(texts, m["text"].(string))
	}
	if !reflect.DeepEqual(ids, []string{"base", "env", "prompter:extra"}) ||
		!reflect.DeepEqual(texts, []string{"BASE [S]", "ENV", "EXTRA m2"}) {
		t.Errorf("$.prompt.compose sections = %v %v, want memory dropped, base rewritten, the hook's section last\n%s", ids, texts, h.logs)
	}
	if n := len(seenOf(t, h, "prompt.compose")); n != 1 {
		t.Errorf("compose hooks saw %d events, want 1", n)
	}
	if n := len(seenOf(t, h, "prompt.section")); n != 3 {
		t.Errorf("section hooks saw %d events, want one per section", n)
	}

	// The engine's own render of the system prompt takes the same path.
	got := h.e.ComposePrompt(ctx, facts, base)
	if len(got) != 3 || got[0].Text != "BASE [S]" || got[2].ID != "prompter:extra" {
		t.Errorf("ComposePrompt = %+v", got)
	}
}

func TestPromptComposeRefusesABadList(t *testing.T) {
	if _, ok := sectionsOf([]any{
		map[string]any{"id": "a", "text": "x", "scope": "session"},
		map[string]any{"id": "b", "text": "y", "scope": "shared"},
	}); ok {
		t.Error("a shared section after a session one was accepted")
	}
	if _, ok := sectionsOf([]any{
		map[string]any{"id": "a", "text": "x", "scope": "session"},
		map[string]any{"id": "a", "text": "y", "scope": "session"},
	}); ok {
		t.Error("a repeated id was accepted")
	}
}

func TestUTF16Offsets(t *testing.T) {
	s := "a😀b"
	if got := RuneToU16(s, 2); got != 3 {
		t.Errorf("RuneToU16 = %d, want 3", got)
	}
	if got := U16ToRune(s, 3); got != 2 {
		t.Errorf("U16ToRune = %d, want 2", got)
	}
	if got := splice16(s, 1, 3, "-"); got != "a-b" {
		t.Errorf("splice16 = %q", got)
	}
}
