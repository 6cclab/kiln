package mods

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// cfgcall calls a $ method from the configger fixture, so the recorder (which
// hooks the same events) sees what that call raised.
func cfgcall(t *testing.T, h *harness, fn string, args ...any) map[string]any {
	t.Helper()
	a, _ := json.Marshal(map[string]any{"fn": fn, "args": args})
	var out map[string]any
	if err := json.Unmarshal([]byte(runCommand(t, h, "cfgcall", string(a))), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func configHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, Options{Services: Services{
		Config: func() []ConfigRow {
			return []ConfigRow{
				{Key: "model", Label: "Model", Kind: "text", Value: "m1", Via: "/model"},
				{Key: "permissionMode", Label: "Permission mode", Kind: "choice", Value: "manual",
					Options: []string{"manual", "plan"}, Via: "/plan"},
			}
		},
	}})
	m := fixture(t, "configured")
	m.Options = map[string]any{"loud": true}
	if errs := h.e.Load([]Module{fixture(t, "configger"), fixture(t, "recorder"), m}); len(errs) > 0 {
		t.Fatal(errs)
	}
	h.e.SessionStart(ctxT(t))
	return h
}

func TestConfigListRunsDescribeHooksAndAddsPluginRows(t *testing.T) {
	h := configHarness(t)
	rows, _ := call(t, h, "config.list")["ok"].([]any)
	byKey := map[string]map[string]any{}
	var keys []string
	for _, r := range rows {
		m := r.(map[string]any)
		byKey[m["key"].(string)] = m
		keys = append(keys, m["key"].(string))
	}
	want := []string{"model", "configured.bare", "configured.greeting", "configured.loud"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("config.list keys = %v, want %v (permissionMode hidden by a hook)\n%s", keys, want, h.logs)
	}
	model := byKey["model"]
	if model["label"] != "Model!" || model["description"] != "the model in use" || model["value"] != "m1" ||
		model["kind"] != "text" || model["isLocked"] != false ||
		!reflect.DeepEqual(model["provider"], map[string]any{"plugin": "engine", "tier": "core"}) {
		t.Errorf("model row = %v", model)
	}
	loud := byKey["configured.loud"]
	if loud["kind"] != "boolean" || loud["value"] != true || loud["isLocked"] != true ||
		!reflect.DeepEqual(loud["provider"], map[string]any{"plugin": "configured", "tier": "user"}) {
		t.Errorf("a plugin's userConfig row = %v, want a locked boolean valued from its options", loud)
	}
	if byKey["configured.greeting"]["value"] != "hi" {
		t.Errorf("greeting = %v, want the manifest default", byKey["configured.greeting"])
	}
	// describe is not the caller's event, so the recorder's own hook runs too.
	if n := len(seenOf(t, h, "config.describe")); n != 5 {
		t.Errorf("recorder saw %d config.describe events, want one per row (5)", n)
	}
}

func writeManifest(t *testing.T, dir, manifest string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigSetThroughHooksNeverWrites(t *testing.T) {
	h := configHarness(t)
	deny := func(out map[string]any) string {
		t.Helper()
		res, _ := out["ok"].(map[string]any)
		s, _ := res["deny"].(string)
		if _, has := res["value"]; has || s == "" {
			t.Errorf("config.set = %v, want { deny } and never { value }: kiln writes no row", out)
		}
		return s
	}
	if got := deny(call(t, h, "config.set", map[string]any{"key": "model", "value": "m2"})); got != "the model stays" {
		t.Errorf("a hook's deny = %q, want it passed through", got)
	}
	// The hook clamps a bad value to "plan"; the row then fits and only /plan changes it.
	if got := deny(call(t, h, "config.set", map[string]any{"key": "permissionMode", "value": "bogus"})); got != "only /plan changes permissionMode" {
		t.Errorf("clamped value's answer = %q", got)
	}
	// A hook answering { value } without core is not a write.
	if got := deny(call(t, h, "config.set", map[string]any{"key": "configured.greeting", "value": "x"})); got != noConfigWriter {
		t.Errorf("a hook's { value } answer = %q, want kiln's no-writer deny", got)
	}
	if got := deny(call(t, h, "config.set", map[string]any{"key": "configured.loud", "value": false})); !strings.Contains(got, "never writes them") {
		t.Errorf("a plugin row's answer = %q, want the locked-source deny", got)
	}
	if out := call(t, h, "config.set", map[string]any{"key": "nope", "value": 1}); !strings.Contains(out["err"].(string), `no /config row is named "nope"`) {
		t.Errorf("an unknown key = %v, want a rejection", out)
	}
	if out := call(t, h, "config.set", map[string]any{"key": "model"}); out["err"] == nil {
		t.Errorf("a call without a value = %v, want a rejection", out)
	}
}

func TestConfigSetValueMustFitTheRow(t *testing.T) {
	h := newHarness(t, Options{Services: Services{Config: func() []ConfigRow {
		return []ConfigRow{{Key: "n", Label: "N", Kind: "number", Value: 1.0, Via: "/n"},
			{Key: "b", Label: "B", Kind: "boolean", Value: true}, {Key: "c", Label: "C", Kind: "choice", Value: "a", Options: []string{"a"}},
			{Key: "l", Label: "L", Kind: "text", Value: []any{"x"}}}
	}}})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	for _, c := range []struct {
		key   string
		value any
		want  string
	}{
		{"n", "str", "does not fit the number row n"},
		{"n", 2, "only /n changes n"},
		{"b", "yes", "does not fit the boolean row b"},
		{"b", false, noConfigWriter},
		{"c", "z", "does not fit the choice row c"},
		{"c", "a", noConfigWriter},
		{"l", []any{"y"}, noConfigWriter},
		{"l", []any{1}, "does not fit the text row l"},
		{"l", "s", noConfigWriter},
		{"l", true, "does not fit the text row l"},
	} {
		res, _ := call(t, h, "config.set", map[string]any{"key": c.key, "value": c.value})["ok"].(map[string]any)
		if got, _ := res["deny"].(string); !strings.Contains(got, c.want) {
			t.Errorf("config.set(%s, %v) = %q, want %q", c.key, c.value, got, c.want)
		}
	}
}

func TestConfigSetRaisesEventWithPluginOrigin(t *testing.T) {
	h := configHarness(t)
	cfgcall(t, h, "config.set", map[string]any{"key": "permissionMode", "value": "plan"})
	got := seenOf(t, h, "config.set")
	if len(got) != 1 {
		t.Fatalf("recorder saw %d config.set events, want 1\n%s", len(got), h.logs)
	}
	want := map[string]any{"key": "permissionMode", "value": "plan", "previous": "manual",
		"provider": map[string]any{"plugin": "engine", "tier": "core"},
		"origin":   map[string]any{"kind": "plugin", "name": "configger"}}
	if !reflect.DeepEqual(got[0]["e"], want) {
		t.Errorf("config.set e = %v, want %v", got[0]["e"], want)
	}
}

func TestTelemetryLogAndMarkRaiseEvents(t *testing.T) {
	h := configHarness(t)
	props := map[string]any{"answer": 2, "ok": true, "page": map[string]any{"value": "ready", "of": []any{"ready", "later"}}}
	if out := cfgcall(t, h, "telemetry.log", map[string]any{"event": "survey_answered", "props": props}); out["err"] != nil || out["ok"] != nil {
		t.Errorf("telemetry.log = %v, want it resolved with nothing", out)
	}
	cfgcall(t, h, "telemetry.mark", map[string]any{"feature": "learn_page", "kind": "sad", "reason": "blocked", "props": map[string]any{"n": 1}})
	logs := seenOf(t, h, "telemetry.log")
	if len(logs) != 1 || logs[0]["e"].(map[string]any)["to"] != "anthropic" || logs[0]["e"].(map[string]any)["event"] != "survey_answered" {
		t.Errorf("telemetry.log events = %v, want one with to filled in as anthropic", logs)
	}
	marks := seenOf(t, h, "telemetry.mark")
	wantMark := map[string]any{"feature": "learn_page", "kind": "sad", "reason": "blocked", "props": map[string]any{"n": float64(1)}}
	if len(marks) != 1 || !reflect.DeepEqual(marks[0]["e"], wantMark) {
		t.Errorf("telemetry.mark events = %v, want %v", marks, wantMark)
	}
	if strings.Contains(h.logs.String(), "does not raise telemetry") {
		t.Errorf("an on(\"telemetry.*\") hook was reported as never running:\n%s", h.logs)
	}
}

func TestTelemetryHooksRewriteAndDeny(t *testing.T) {
	h := configHarness(t)
	rec := map[string]any{"to": "collector", "event": "e1", "attributes": map[string]any{"a": 1}}
	if out := call(t, h, "telemetry.log", rec); out["err"] != nil {
		t.Fatalf("collector record = %v\n%s", out, h.logs)
	}
	var tagged []map[string]any
	if err := json.Unmarshal([]byte(runCommand(t, h, "cfgdump", "")), &tagged); err != nil {
		t.Fatal(err)
	}
	if len(tagged) != 1 || tagged[0]["to"] != "collector" || !reflect.DeepEqual(tagged[0]["attributes"], map[string]any{"a": float64(1), "tagged": true}) {
		t.Errorf("the hook saw %v, want the record with its attributes", tagged)
	}
	if at, _ := tagged[0]["loggedAt"].(string); !strings.HasPrefix(at, "2026-10-15T12:00:00.000Z") {
		t.Errorf("loggedAt = %v, want the engine's clock filled in", tagged[0]["loggedAt"])
	}
	out := call(t, h, "telemetry.mark", map[string]any{"feature": "f", "kind": "bad"})
	if err, _ := out["err"].(string); !strings.Contains(err, "no bad marks") {
		t.Errorf("a denied mark = %v, want the call rejected with the hook's reason", out)
	}
}

func TestTelemetryRejectsMalformedEntries(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	h.e.SessionStart(ctxT(t))
	for _, c := range []struct {
		fn   string
		arg  any
		want string
	}{
		{"telemetry.log", map[string]any{}, "event name"},
		{"telemetry.log", map[string]any{"event": "e", "to": "elsewhere"}, `to is "anthropic" or "collector"`},
		{"telemetry.log", map[string]any{"event": "e", "to": "collector"}, "attributes"},
		{"telemetry.log", map[string]any{"event": "e", "props": map[string]any{"p": "str"}}, "props.p"},
		{"telemetry.log", map[string]any{"event": "e", "props": map[string]any{"p": map[string]any{"value": "z", "of": []any{"a"}}}}, `"z" is not in its "of" list`},
		{"telemetry.log", map[string]any{"event": "e", "props": 3}, "props is an object"},
		{"telemetry.mark", map[string]any{"feature": "Bad Name", "kind": "ok"}, "snake_case"},
		{"telemetry.mark", map[string]any{"feature": "f", "kind": "meh"}, `"ok", "sad" or "bad"`},
		{"telemetry.mark", map[string]any{"feature": "f", "kind": "ok", "props": map[string]any{"p": nil}}, "props.p"},
	} {
		if out := call(t, h, c.fn, c.arg); !strings.Contains(String(out["err"]), c.want) {
			t.Errorf("%s(%v) = %v, want a rejection containing %q", c.fn, c.arg, out, c.want)
		}
	}
	if out := call(t, h, "telemetry.log"); out["err"] == nil {
		t.Errorf("telemetry.log() = %v, want a rejection", out)
	}
}

// String is v as a string ("" for anything else).
func String(v any) string { s, _ := v.(string); return s }

func TestSkillPromptRewritesAndPinsFields(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "configger", "recorder")
	h.e.SessionStart(ctxT(t))
	ctx := ctxT(t)
	in := SkillPromptInput{Skill: "commit", Text: "Write a commit.", Source: "userSettings", Path: "/s/commit/SKILL.md",
		Frontmatter: map[string]any{"name": "commit", "__proto__": "x"}}
	if got := h.e.SkillPrompt(ctx, in); got != "Write a commit. [haiku]" {
		t.Errorf("SkillPrompt = %q, want the hook's rewrite", got)
	}
	seen := seenOf(t, h, "skill.prompt")
	if len(seen) != 1 {
		t.Fatalf("recorder saw %d skill.prompt events, want 1\n%s", len(seen), h.logs)
	}
	want := map[string]any{"skill": "commit", "text": "Write a commit.", "source": "userSettings", "path": "/s/commit/SKILL.md",
		"frontmatter": map[string]any{"name": "commit"}, "frontmatterProblem": "the frontmatter key __proto__ is left out"}
	if !reflect.DeepEqual(seen[0]["e"], want) {
		t.Errorf("skill.prompt e = %v, want %v", seen[0]["e"], want)
	}
	if got := h.e.SkillPrompt(ctx, SkillPromptInput{Skill: "other", Text: "same", Plugin: "p"}); got != "same" {
		t.Errorf("an unmatched skill = %q, want its text untouched", got)
	}
	if e := seenOf(t, h, "skill.prompt")[1]["e"].(map[string]any); e["source"] != "built-in" || e["pluginName"] != "p" || e["path"] != nil {
		t.Errorf("a skill with no source = %v, want built-in and its plugin", e)
	}
	if got := h.e.SkillPrompt(ctx, SkillPromptInput{Skill: "pinned", Text: "t", Source: "userSettings"}); !strings.HasPrefix(got, "refused: ") || !strings.Contains(got, "source is pinned") {
		t.Errorf("a hook rewriting the pinned source got %q, want the rewrite refused", got)
	}
}

func TestSkillPromptWithoutHooksLeavesTheText(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "describer")
	if got := h.e.SkillPrompt(ctxT(t), SkillPromptInput{Skill: "commit", Text: "as written"}); got != "as written" {
		t.Errorf("SkillPrompt with no hook = %q", got)
	}
	var nilEngine *Engine
	if got := nilEngine.SkillPrompt(ctxT(t), SkillPromptInput{Text: "x"}); got != "x" {
		t.Errorf("nil engine SkillPrompt = %q", got)
	}
}

func TestAttributionTextIsNotRaised(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "recorder")
	if !strings.Contains(h.logs.String(), "kiln does not raise attribution.text") {
		t.Errorf("a hook on attribution.text was not reported as never running (kiln composes no commit or PR text):\n%s", h.logs)
	}
	for _, ev := range []string{"config.set", "config.describe", "telemetry.log", "telemetry.mark", "skill.prompt"} {
		if strings.Contains(h.logs.String(), "kiln does not raise "+ev) {
			t.Errorf("%s is raised by kiln but was reported as never running", ev)
		}
	}
}

func TestExpandPattern(t *testing.T) {
	if got := expandPattern("telemetry.*"); !reflect.DeepEqual(got, []string{"telemetry.log", "telemetry.mark"}) {
		t.Errorf("telemetry.* = %v", got)
	}
	if got := expandPattern("tool.call"); !reflect.DeepEqual(got, []string{"tool.call"}) {
		t.Errorf("a plain event = %v", got)
	}
	if got := expandPattern("nothing.*"); !reflect.DeepEqual(got, []string{"nothing.*"}) {
		t.Errorf("a noun kiln raises none of = %v, want it left as written", got)
	}
}

func TestPluginConfigRowsSkipSensitiveAndTypeFields(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"p","userConfig":{
		"token":{"type":"string","sensitive":true},
		"n":{"type":"number","title":"Count"},
		"mode":{"type":"string","options":["a","b"],"description":"which"},
		"dirs":{"type":"string","multiple":true}}}`)
	rows := pluginConfigRows("p", dir, map[string]any{"n": 3.0, "token": "secret"})
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
		if r.Key == "p.token" {
			t.Error("a sensitive field was listed: its value is a secret")
		}
	}
	if !reflect.DeepEqual(keys, []string{"p.dirs", "p.mode", "p.n"}) {
		t.Fatalf("keys = %v", keys)
	}
	byKey := map[string]configRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if r := byKey["p.n"]; r.Kind != "number" || r.Label != "Count" || r.Value != 3.0 {
		t.Errorf("number row = %+v", r)
	}
	if r := byKey["p.mode"]; r.Kind != "choice" || !reflect.DeepEqual(r.Options, []string{"a", "b"}) || r.Description != "which" {
		t.Errorf("choice row = %+v", r)
	}
	if r := byKey["p.dirs"]; r.Kind != "text" || !reflect.DeepEqual(r.Value, []any{}) {
		t.Errorf("multiple row = %+v", r)
	}
	if got := pluginConfigRows("p", t.TempDir(), nil); got != nil {
		t.Errorf("a plugin with no manifest listed %v", got)
	}
}
