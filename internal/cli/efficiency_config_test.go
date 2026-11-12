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

func TestEfficiencyConfigReportsWorkerShellAndProgressLoopProvenance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".kiln", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"orchestration":{"isolatedWorkers":true,"isolatedShell":true,"autoIntegrate":true},"runtimeEfficiency":{"progressLoopLimit":3}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := EfficiencyConfig(Args{}, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var report struct {
		Orchestration map[string]any    `json:"orchestration"`
		Runtime       map[string]any    `json:"runtimeEfficiency"`
		Sources       map[string]string `json:"sources"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Orchestration["isolatedShell"] != true || report.Orchestration["autoIntegrate"] != true || report.Runtime["progressLoopLimit"] != float64(3) {
		t.Fatalf("effective controls absent: %s", out.String())
	}
	for _, key := range []string{"orchestration.isolatedShell", "orchestration.autoIntegrate", "runtimeEfficiency.progressLoopLimit"} {
		if got := report.Sources[key]; got != settingsPath {
			t.Fatalf("%s source = %q, want %q", key, got, settingsPath)
		}
	}
}

func TestEfficiencyConfigReportsOtherSettingSources(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"model":"faux/faux-1","autoMemoryEnabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := EfficiencyConfig(Args{}, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var report struct {
		Sources    map[string]string `json:"sources"`
		Settings   map[string]any    `json:"contextSettings"`
		Configured map[string]any    `json:"configuredSettings"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Sources["model"] != path || report.Sources["autoMemoryEnabled"] != path || report.Sources["effortLevel"] != "default" || report.Settings["model"] != "faux/faux-1" || report.Configured["autoMemoryEnabled"] != false {
		t.Fatal(out.String())
	}
}

func TestEfficiencyConfigRuntimeOverridePrecedenceAndRejectedMemoryDir(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	file := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"model":"faux/faux-1","effortLevel":"low","permissions":{"defaultMode":"acceptEdits"},"autoMemoryDirectory":"/"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	args := Args{Model: "faux/faux-2", Effort: "high", PermissionMode: "plan"}
	if code := EfficiencyConfig(args, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var report struct {
		Sources  map[string]string `json:"sources"`
		Settings map[string]any    `json:"contextSettings"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Settings["model"] != "faux/faux-2" || report.Sources["model"] != "--model" || report.Settings["effortLevel"] != "high" || report.Sources["effortLevel"] != "--effort" || report.Settings["permissionMode"] != "plan" || report.Sources["permissions.defaultMode"] != "--permission-mode" {
		t.Fatal(out.String())
	}
	if report.Sources["autoMemoryDirectory"] != "default" || report.Settings["autoMemoryDirectory"] == "/" {
		t.Fatal(out.String())
	}
}

func TestEfficiencyConfigEnvironmentAndUntrustedDirectoryPrecedence(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HARNESS_MODEL", "faux/faux-2")
	t.Setenv("HARNESS_PERMISSION_MODE", "dontAsk")
	t.Chdir(cwd)
	userFile := filepath.Join(home, ".claude", "settings.json")
	projectFile := filepath.Join(cwd, ".claude", "settings.json")
	for file, contents := range map[string]string{
		userFile:    `{"model":"opus","autoMemoryDirectory":"~/accepted-memory"}`,
		projectFile: `{"autoMemoryDirectory":"/project-memory"}`,
	} {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, stderr bytes.Buffer
	if code := EfficiencyConfig(Args{}, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var report struct {
		Sources  map[string]string `json:"sources"`
		Settings map[string]any    `json:"contextSettings"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Settings["model"] != "faux/faux-2" || report.Sources["model"] != "HARNESS_MODEL" || report.Settings["permissionMode"] != "dontAsk" || report.Sources["permissions.defaultMode"] != "HARNESS_PERMISSION_MODE" {
		t.Fatal(out.String())
	}
	if report.Settings["autoMemoryDirectory"] != filepath.Join(home, "accepted-memory") || report.Sources["autoMemoryDirectory"] != userFile {
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
