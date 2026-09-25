package scenario_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/scenario"
)

// writeFile creates path's parent directories, then writes data with the
// given mode.
func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	t.Run("valid scenario", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "faux.yaml"), []byte("model: faux-1\n"), 0o644)
		writeFile(t, filepath.Join(dir, "scenario.yaml"), []byte(`
description: fixes the bug
tags: [smoke, quick]
fixture: mathbug
prompt: fix the bug in math.js
permission_mode: acceptEdits
allowed_tools: [read, edit]
settings:
  permissions:
    allow: ["Bash(rtk:*)"]
caps:
  max_turns: 8
  max_tokens: 100000
  max_cost_usd: 0.50
  timeout: 90s
checks:
  - type: file_matches
    path: src/math.js
    regex: "return a \\+ b"
  - type: result_ok
judge:
  rubric: "did it fix the bug"
  faux_verdict:
    pass: true
    score: 1
faux:
  anthropic-messages: faux.yaml
`), 0o644)

		s, err := scenario.Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.Name != filepath.Base(dir) {
			t.Errorf("Name = %q, want dir base name %q", s.Name, filepath.Base(dir))
		}
		if s.Description != "fixes the bug" {
			t.Errorf("Description = %q", s.Description)
		}
		if len(s.Tags) != 2 || s.Tags[0] != "smoke" {
			t.Errorf("Tags = %v", s.Tags)
		}
		if s.Fixture != "mathbug" {
			t.Errorf("Fixture = %q", s.Fixture)
		}
		if s.Prompt != "fix the bug in math.js" {
			t.Errorf("Prompt = %q", s.Prompt)
		}
		if s.Caps.MaxTurns != 8 || s.Caps.MaxTokens != 100000 || s.Caps.MaxCostUSD != 0.50 {
			t.Errorf("Caps = %+v", s.Caps)
		}
		if s.Caps.Timeout != 90*time.Second {
			t.Errorf("Caps.Timeout = %v, want 90s", s.Caps.Timeout)
		}
		if len(s.Checks) != 2 || s.Checks[0].Type != "file_matches" || s.Checks[0].Path != "src/math.js" {
			t.Errorf("Checks = %+v", s.Checks)
		}
		if s.Judge == nil || s.Judge.Rubric != "did it fix the bug" {
			t.Fatalf("Judge = %+v", s.Judge)
		}
		var verdict struct {
			Pass  bool `json:"pass"`
			Score int  `json:"score"`
		}
		if err := json.Unmarshal(s.Judge.FauxVerdict, &verdict); err != nil {
			t.Fatalf("FauxVerdict not valid JSON: %v (%s)", err, s.Judge.FauxVerdict)
		}
		if !verdict.Pass || verdict.Score != 1 {
			t.Errorf("verdict = %+v", verdict)
		}
		if s.Dir != dir {
			t.Errorf("Dir = %q, want %q", s.Dir, dir)
		}

		got, err := s.FauxScript("")
		if err != nil {
			t.Fatalf("FauxScript(\"\"): %v", err)
		}
		if got != "model: faux-1\n" {
			t.Errorf("FauxScript = %q", got)
		}
		got, err = s.FauxScript("anthropic-messages")
		if err != nil {
			t.Fatalf("FauxScript(anthropic-messages): %v", err)
		}
		if got != "model: faux-1\n" {
			t.Errorf("FauxScript(anthropic-messages) = %q", got)
		}
	})

	t.Run("missing prompt", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "scenario.yaml"), []byte("description: no prompt\n"), 0o644)
		if _, err := scenario.Load(dir); err == nil {
			t.Fatal("expected an error for a missing prompt")
		}
	})

	t.Run("unknown check type", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "scenario.yaml"), []byte(`
prompt: do the thing
checks:
  - type: not_a_real_check
`), 0o644)
		_, err := scenario.Load(dir)
		if err == nil {
			t.Fatal("expected an error for an unknown check type")
		}
	})

	t.Run("missing faux file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "scenario.yaml"), []byte(`
prompt: do the thing
faux:
  anthropic-messages: does-not-exist.yaml
`), 0o644)
		_, err := scenario.Load(dir)
		if err == nil {
			t.Fatal("expected an error for a missing faux script file")
		}
	})

	t.Run("default name from dir", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "my-scenario")
		writeFile(t, filepath.Join(sub, "scenario.yaml"), []byte("prompt: hi\n"), 0o644)
		s, err := scenario.Load(sub)
		if err != nil {
			t.Fatal(err)
		}
		if s.Name != "my-scenario" {
			t.Errorf("Name = %q, want %q", s.Name, "my-scenario")
		}
	})
}

func TestList(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "zeta", "scenario.yaml"), []byte("name: zeta\nprompt: z\n"), 0o644)
	writeFile(t, filepath.Join(root, "alpha", "scenario.yaml"), []byte("name: alpha\nprompt: a\n"), 0o644)
	writeFile(t, filepath.Join(root, "mid", "scenario.yaml"), []byte("name: mid\nprompt: m\n"), 0o644)
	// A subdirectory without a scenario.yaml must be skipped, not error.
	if err := os.MkdirAll(filepath.Join(root, "not-a-scenario"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "not-a-scenario", "README.md"), []byte("nothing to see here"), 0o644)

	got, err := scenario.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		names := make([]string, len(got))
		for i, s := range got {
			names[i] = s.Name
		}
		t.Fatalf("expected 3 scenarios, got %d: %v", len(got), names)
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, s := range got {
		if s.Name != want[i] {
			t.Errorf("scenario %d: Name = %q, want %q", i, s.Name, want[i])
		}
	}
}

func TestCopyFixture(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "top.txt"), []byte("top"), 0o644)
	writeFile(t, filepath.Join(src, "nested", "deep.txt"), []byte("deep"), 0o644)
	writeFile(t, filepath.Join(src, "nested", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755)
	// A .git directory must be skipped entirely.
	writeFile(t, filepath.Join(src, ".git", "config"), []byte("should not be copied"), 0o644)

	dst := filepath.Join(t.TempDir(), "copy")
	if err := scenario.CopyFixture(src, dst); err != nil {
		t.Fatalf("CopyFixture: %v", err)
	}

	top, err := os.ReadFile(filepath.Join(dst, "top.txt"))
	if err != nil || string(top) != "top" {
		t.Fatalf("top.txt = %q, %v", top, err)
	}
	deep, err := os.ReadFile(filepath.Join(dst, "nested", "deep.txt"))
	if err != nil || string(deep) != "deep" {
		t.Fatalf("nested/deep.txt = %q, %v", deep, err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git should not have been copied, stat err = %v", err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dst, "nested", "run.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("run.sh lost its executable bit: mode = %v", info.Mode())
		}
	}
}

func TestWriteSettings(t *testing.T) {
	proj := t.TempDir()

	existing := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"Bash(rtk:*)"},
			"deny":  []any{"Bash(rm:*)"},
		},
		"otherTopLevel": "keep me",
	}
	writeFile(t, filepath.Join(proj, ".claude", "settings.json"), mustJSON(t, existing), 0o644)

	overlay := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"Bash(go:*)"}, // replaces permissions.allow wholesale (one level deep)
		},
		"modelRoles": map[string]any{
			"planner": "opus",
		},
	}
	if err := scenario.WriteSettings(proj, overlay); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(proj, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}

	if got["otherTopLevel"] != "keep me" {
		t.Errorf("otherTopLevel = %v, want it preserved", got["otherTopLevel"])
	}
	perms, ok := got["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions = %#v, want a map", got["permissions"])
	}
	allow, ok := perms["allow"].([]any)
	if !ok || len(allow) != 1 || allow[0] != "Bash(go:*)" {
		t.Errorf("permissions.allow = %v, want overlay's value to win", perms["allow"])
	}
	if deny, ok := perms["deny"].([]any); !ok || len(deny) != 1 || deny[0] != "Bash(rm:*)" {
		t.Errorf("permissions.deny = %v, want the existing value preserved (one-level merge)", perms["deny"])
	}
	roles, ok := got["modelRoles"].(map[string]any)
	if !ok || roles["planner"] != "opus" {
		t.Errorf("modelRoles = %v", got["modelRoles"])
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestLoadParsesLiveTimeout guards the split between the faux cap and the
// live cap: a live run must never inherit the faux timeout.
func TestLoadParsesLiveTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte("prompt: p\ncaps:\n  timeout: 60s\n  live_timeout: 15m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := scenario.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Caps.Timeout != 60*time.Second || sc.Caps.LiveTimeout != 15*time.Minute {
		t.Fatalf("caps = %+v, want timeout 60s and live_timeout 15m", sc.Caps)
	}
}
