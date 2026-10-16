package settings

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
)

// With no modelRouter key the router is not "set", runs on the default
// roles, and takes modelRoles only from user settings and --settings.
func TestLoadModelRouter_DefaultsAndTrustedRoles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(home, ".claude", "settings.json"), `{"modelRoles": {"fast": "faux/faux-2", "structured": "faux/faux-1"}}`)
	writeJSONFile(t, filepath.Join(home, ".kiln", "settings.json"), `{"modelRoles": {"heavy": "faux/faux-3"}}`)
	writeJSONFile(t, filepath.Join(cwd, ".claude", "settings.json"), `{"modelRoles": {"fast": "anthropic/claude-opus-4-1"}}`)

	cfg := LoadModelRouter(cwd, LoadOptions{})
	if cfg.Set || cfg.Disabled {
		t.Errorf("Set/Disabled = %v/%v, want an unset router", cfg.Set, cfg.Disabled)
	}
	want := map[string]string{"fast": "faux/faux-2", "structured": "faux/faux-1", "heavy": "faux/faux-3"}
	if !reflect.DeepEqual(cfg.ModelRoles, want) {
		t.Errorf("ModelRoles = %v, want %v (user scope only)", cfg.ModelRoles, want)
	}
	for _, slot := range RouterSlots {
		if got := cfg.RoleFor(slot); got != DefaultRouterRoles[slot] {
			t.Errorf("RoleFor(%s) = %q, want the default %q", slot, got, DefaultRouterRoles[slot])
		}
	}
	if !cfg.Classifier || cfg.Fallback != RouterFallbackPlan {
		t.Errorf("Classifier/Fallback = %v/%q, want true/plan", cfg.Classifier, cfg.Fallback)
	}
}

// A project or local modelRouter is ignored and reported; a user one is
// read, and --settings overrides it.
func TestLoadModelRouter_ProjectIgnoredFlagWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"modelRouter": {"roles": {"plan": "fast", "implement": "fast"}, "classifier": false, "fallback": "heuristic"}}`)
	writeJSONFile(t, filepath.Join(cwd, ".claude", "settings.json"), `{"modelRouter": {"roles": {"plan": "heavy"}}}`)
	writeJSONFile(t, filepath.Join(cwd, ".claude", "settings.local.json"), `{"modelRouter": false}`)

	cfg := LoadModelRouter(cwd, LoadOptions{})
	if !cfg.Set || cfg.Disabled {
		t.Fatalf("Set/Disabled = %v/%v", cfg.Set, cfg.Disabled)
	}
	if got := cfg.RoleFor(RouterSlotPlan); got != "fast" {
		t.Errorf("plan = %q, want the user file's fast (the project's heavy is ignored)", got)
	}
	if got := cfg.RoleFor(RouterSlotPlanComplex); got != "" {
		t.Errorf("planComplex = %q, want unset (named roles replace the defaults)", got)
	}
	if cfg.Classifier || cfg.Fallback != RouterFallbackHeuristic {
		t.Errorf("Classifier/Fallback = %v/%q", cfg.Classifier, cfg.Fallback)
	}
	if len(cfg.Ignored) != 2 {
		t.Errorf("Ignored = %q, want the project and local files", cfg.Ignored)
	}
	if w := ModelRouterIgnoredWarning(cwd, cfg.Ignored); !strings.Contains(w, "modelRouter in .claude/settings.json and other project settings is ignored") {
		t.Errorf("warning = %q", w)
	}

	extra := filepath.Join(t.TempDir(), "flag.json")
	writeJSONFile(t, extra, `{"modelRouter": false}`)
	if cfg := LoadModelRouter(cwd, LoadOptions{Extra: extra}); !cfg.Disabled {
		t.Errorf("--settings modelRouter:false did not disable the router: %+v", cfg)
	}
	// --setting-sources without user: the user file's block is not read.
	if cfg := LoadModelRouter(cwd, LoadOptions{Sources: []paths.Scope{paths.ScopeProject}}); cfg.Set {
		t.Errorf("read the user file's modelRouter with user sources excluded: %+v", cfg)
	}
}

// Values kiln cannot use are reported and skipped.
func TestLoadModelRouter_Problems(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"modelRouter": {"roles": {"plan": "fast", "review": "heavy"}, "fallback": "coin"}}`)
	cfg := LoadModelRouter(cwd, LoadOptions{})
	if cfg.Fallback != RouterFallbackPlan {
		t.Errorf("Fallback = %q, want plan after a bad value", cfg.Fallback)
	}
	if _, ok := cfg.Roles["review"]; ok {
		t.Error("kept an unknown slot")
	}
	if len(cfg.Problems) != 2 || !strings.Contains(cfg.Problems[0], "roles.review") || !strings.Contains(cfg.Problems[1], `"coin"`) {
		t.Errorf("Problems = %q", cfg.Problems)
	}

	writeJSONFile(t, filepath.Join(home, ".claude", "settings.json"), `{"modelRouter": "yes"}`)
	if cfg := LoadModelRouter(cwd, LoadOptions{}); cfg.Set || len(cfg.Problems) != 1 {
		t.Errorf("a string modelRouter: %+v", cfg)
	}
}
