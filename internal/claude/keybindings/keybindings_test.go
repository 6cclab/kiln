package keybindings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzLoad feeds arbitrary bytes, written to a temp keybindings.json, into
// Load. It is a user-editable file, so it must never panic regardless of
// how malformed the JSON is; an Error result is fine.
func FuzzLoad(f *testing.F) {
	seeds := []string{
		"",
		"{}",
		"{ not json",
		"[1,2,3]",
		`{"a":"ctrl+s","b":"ctrl+s"}`,
		"null",
		"true",
		`"just a string"`,
		"42",
		`{"a": 1, "b": true, "c": null, "d": ["x"], "e": {}}`,
		`{"a":"ctrl+s","b":"ctrl+s","c":"ctrl+s"}`,
		`{"unicode\u0000key": "ctrl+é"}`,
		`{"a":"x"` + "\x00",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "keybindings.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		_ = Load(path)
	})
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	t.Run("treats an absent file as normal rather than an error", func(t *testing.T) {
		// Most machines have no keybindings.json; defaults simply apply.
		result := Load(filepath.Join(dir, "missing.json"))
		if result.Loaded {
			t.Error("expected Loaded=false")
		}
		if result.Error != "" {
			t.Errorf("expected no error, got %q", result.Error)
		}
	})

	t.Run("reports a broken file instead of silently using defaults", func(t *testing.T) {
		path := filepath.Join(dir, "broken.json")
		if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		result := Load(path)
		if result.Loaded {
			t.Error("expected Loaded=false")
		}
		if !strings.Contains(result.Error, path) {
			t.Errorf("error %q does not mention path", result.Error)
		}
	})

	t.Run("rejects a file that parses but is not an object", func(t *testing.T) {
		path := filepath.Join(dir, "array.json")
		if err := os.WriteFile(path, []byte("[1,2,3]"), 0o644); err != nil {
			t.Fatal(err)
		}
		if result := Load(path); result.Error == "" {
			t.Error("expected an error")
		}
	})

	t.Run("applies a valid override file", func(t *testing.T) {
		path := filepath.Join(dir, "ok.json")
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		result := Load(path)
		if !result.Loaded {
			t.Error("expected Loaded=true")
		}
		if result.Error != "" {
			t.Errorf("expected no error, got %q", result.Error)
		}
	})

	t.Run("reports conflicts without resolving them", func(t *testing.T) {
		path := filepath.Join(dir, "conflict.json")
		if err := os.WriteFile(path, []byte(`{"a":"ctrl+s","b":"ctrl+s"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		result := Load(path)
		if !result.Loaded {
			t.Fatal("expected Loaded=true")
		}
		if len(result.Conflicts) != 1 || result.Conflicts[0] != "ctrl+s is bound to a and b" {
			t.Errorf("got %v", result.Conflicts)
		}
	})

	t.Run("looks in the same place Claude Code does", func(t *testing.T) {
		if !strings.HasSuffix(Path(), "/.claude/keybindings.json") {
			t.Errorf("got %q", Path())
		}
	})
}
