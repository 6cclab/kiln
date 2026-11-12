package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/learning"
	"github.com/andrepato/harness/internal/memory"
)

func TestEfficiencyConfigDoesNotExposeEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".kiln")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"env":{"TOKEN":"never-print-this"},"skillDiscovery":{"mode":"search"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := EfficiencyConfig(Args{}, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if strings.Contains(out.String(), "never-print-this") || !strings.Contains(out.String(), `"mode": "search"`) {
		t.Fatal(out.String())
	}
}

func TestEfficiencyConfigReportsEffectiveLearnedRouting(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := learning.FileHash(exe)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(filepath.Join(home, ".kiln", "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	active := learning.Active{Policy: learning.Policy{SkillBudgetTokens: 512, ConservativeRouting: true}, Report: learning.Report{Accepted: true, BinaryHash: hash, Models: []string{"kiln/auto"}}}
	raw, err := json.Marshal(active)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), memory.Record{Scope: learning.Scope(memoryScope(cwd)), Key: "active", Kind: "procedure", Text: string(raw), Evidence: "test evidence"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, settings string
		want           bool
	}{
		{"learned", `{}`, true},
		{"explicit false", `{"modelRouter":{"conservative":false}}`, false},
		{"disabled learning", `{"learningEnabled":false}`, false},
		{"custom router", `{"modelRouter":{"roles":{"plan":"heavy"}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(home, ".kiln", "settings.json")
			if err := os.WriteFile(path, []byte(tc.settings), 0600); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			if code := EfficiencyConfig(Args{Model: "kiln/auto"}, &out, &stderr); code != 0 {
				t.Fatal(stderr.String())
			}
			var report struct {
				Conservative bool              `json:"conservativeRouting"`
				Sources      map[string]string `json:"sources"`
			}
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			wantSource := "default"
			if tc.name == "learned" {
				wantSource = "evaluated personal policy"
			}
			if tc.name == "explicit false" {
				wantSource = path
			}
			if report.Sources["modelRouter.conservative"] != wantSource {
				t.Fatalf("source: %s", out.String())
			}
			if report.Conservative != tc.want {
				t.Fatalf("got %s", out.String())
			}
		})
	}
}
