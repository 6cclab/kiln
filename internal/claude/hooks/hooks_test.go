package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMatchesHook(t *testing.T) {
	t.Run("matches the configured tool name regardless of casing", func(t *testing.T) {
		if !MatchesHook("Bash", "bash", true) {
			t.Error("Bash should match bash")
		}
		if !MatchesHook("Read", "read", true) {
			t.Error("Read should match read")
		}
	})

	t.Run("is anchored, so Bash does not match BashOutput", func(t *testing.T) {
		if MatchesHook("Bash", "bashoutput", true) {
			t.Error("Bash matched bashoutput")
		}
		if MatchesHook("Edit", "edit_notebook", true) {
			t.Error("Edit matched edit_notebook")
		}
	})

	t.Run("supports the regex alternation real configs use", func(t *testing.T) {
		if !MatchesHook("Edit|Write", "write", true) {
			t.Error("Edit|Write should match write")
		}
		if MatchesHook("Edit|Write", "read", true) {
			t.Error("Edit|Write matched read")
		}
	})

	t.Run("treats an absent or wildcard matcher as everything", func(t *testing.T) {
		if !MatchesHook("", "anything", true) {
			t.Error("empty matcher should match anything")
		}
		if !MatchesHook("*", "anything", true) {
			t.Error("* should match anything")
		}
		// Events with no tool still match a wildcard group.
		if !MatchesHook("", "", false) {
			t.Error("empty matcher should match no-tool event")
		}
	})

	t.Run("fails closed on a malformed regex", func(t *testing.T) {
		if MatchesHook("[unclosed", "bash", true) {
			t.Error("malformed regex matched")
		}
	})
}

func TestLoadHooks(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ".claude"))
	mustWriteJSON(t, filepath.Join(dir, ".claude", "settings.json"), map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{"matcher": "Bash", "hooks": []map[string]any{{"type": "command", "command": "a"}}},
			},
		},
	})
	mustWriteJSON(t, filepath.Join(dir, ".claude", "settings.local.json"), map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{"matcher": "Bash", "hooks": []map[string]any{{"type": "command", "command": "b"}}},
			},
		},
	})

	t.Run("accumulates hooks across scopes instead of overriding", func(t *testing.T) {
		config := LoadHooks(dir)
		commands := HooksFor(config, PreToolUse, "bash", true)
		hasA, hasB := false, false
		var got []string
		for _, c := range commands {
			got = append(got, c.Command)
			if c.Command == "a" {
				hasA = true
			}
			if c.Command == "b" {
				hasB = true
			}
		}
		if !hasA || !hasB {
			t.Errorf("got %v, want both a and b", got)
		}
	})
}

// TestUnsupportedEvents_ReportsEventsKilnDoesNotFire checks a settings.json
// hook config for an event Claude Code recognizes but kiln does not (e.g.
// PermissionRequest) is flagged by name, while a known event (PreToolUse)
// and an event with no hooks registered under it are not — the regression
// for qa/findings/20261004T205021Z-doctor-misses-sandbox-and-hook-
// events.json: such events used to be silently dropped from /doctor's
// hook count with no note that kiln simply doesn't fire them.
func TestUnsupportedEvents_ReportsEventsKilnDoesNotFire(t *testing.T) {
	cfg := Config{
		PreToolUse:                 []Matcher{{Hooks: []Command{{Type: "command", Command: "echo hi"}}}},
		Event("PermissionRequest"): []Matcher{{Hooks: []Command{{Type: "command", Command: "echo pr"}}}},
		Event("PostCompact"):       []Matcher{{Hooks: []Command{{Type: "command", Command: "echo pc"}}}},
		Event("StopFailure"):       nil, // no hooks actually registered
	}
	got := UnsupportedEvents(cfg)
	want := []Event{"PermissionRequest", "PostCompact"}
	if len(got) != len(want) {
		t.Fatalf("UnsupportedEvents = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("UnsupportedEvents[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestUnsupportedEvents_EmptyForKnownOnly checks a config using only the
// nine known events reports nothing unsupported.
func TestUnsupportedEvents_EmptyForKnownOnly(t *testing.T) {
	cfg := Config{
		PreToolUse: []Matcher{{Hooks: []Command{{Type: "command", Command: "echo hi"}}}},
		Stop:       []Matcher{{Hooks: []Command{{Type: "command", Command: "echo bye"}}}},
	}
	if got := UnsupportedEvents(cfg); len(got) != 0 {
		t.Errorf("UnsupportedEvents = %v, want none", got)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
