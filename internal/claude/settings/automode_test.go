package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
)

func writeJSONFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// autoModeFixture writes an autoMode block naming its own file into every
// settings file kiln reads, plus a --settings file.
func autoModeFixture(t *testing.T) (cwd, extra string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd = t.TempDir()
	block := func(name string) string {
		return `{"autoMode": {"environment": ["env from ` + name + `"], "allow": ["allow from ` + name + `"], "soft_deny": ["soft from ` + name + `"], "hard_deny": ["hard from ` + name + `"]}}`
	}
	writeJSONFile(t, filepath.Join(home, ".claude", "settings.json"), block("claude user"))
	writeJSONFile(t, filepath.Join(home, ".kiln", "settings.json"), block("kiln user"))
	writeJSONFile(t, filepath.Join(cwd, ".claude", "settings.json"), block("project"))
	writeJSONFile(t, filepath.Join(cwd, ".claude", "settings.local.json"), block("claude local"))
	writeJSONFile(t, filepath.Join(cwd, ".kiln", "settings.local.json"), block("kiln local"))
	extra = filepath.Join(t.TempDir(), "flag.json")
	writeJSONFile(t, extra, block("flag"))
	return cwd, extra
}

// A repository's own settings files must not be able to tell the classifier
// what to allow: only user settings and --settings count.
func TestLoadAutoMode_ReadsOnlyUserAndFlagSettings(t *testing.T) {
	cwd, extra := autoModeFixture(t)
	cfg := LoadAutoMode(cwd, LoadOptions{Extra: extra})

	want := []string{"allow from claude user", "allow from kiln user", "allow from flag"}
	if !reflect.DeepEqual(cfg.Allow, want) {
		t.Errorf("Allow = %q, want %q", cfg.Allow, want)
	}
	for _, list := range [][]string{cfg.Environment, cfg.Allow, cfg.SoftDeny, cfg.HardDeny} {
		for _, e := range list {
			if strings.Contains(e, "project") || strings.Contains(e, "local") {
				t.Errorf("read a repository settings entry: %q", e)
			}
		}
	}
	if len(cfg.SoftDeny) != 3 || len(cfg.HardDeny) != 3 || len(cfg.Environment) != 3 {
		t.Errorf("lists = %+v", cfg)
	}
	if len(cfg.Ignored) != 3 {
		t.Errorf("Ignored = %q, want the three project/local files", cfg.Ignored)
	}
	if w := AutoModeIgnoredWarning(cwd, cfg.Ignored); !strings.Contains(w, "is ignored") {
		t.Errorf("warning = %q", w)
	}
}

// The classifier's model comes from modelRoles.fast in user settings or
// --settings only: a repository must not pick the model that reviews the
// agent. A project or local fast role is reported, not used.
func TestLoadAutoMode_FastRoleOnlyFromTrustedFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, ".claude", "settings.json"), `{"modelRoles": {"fast": "ollama/tiny"}}`)
	writeJSONFile(t, filepath.Join(cwd, ".kiln", "settings.local.json"), `{"modelRoles": {"fast": "ollama/tinier"}}`)

	cfg := LoadAutoMode(cwd, LoadOptions{})
	if cfg.FastRole != "" {
		t.Errorf("FastRole = %q from project settings, want none", cfg.FastRole)
	}
	if len(cfg.FastRoleIgnored) != 2 || cfg.Ignored != nil {
		t.Errorf("FastRoleIgnored = %q, Ignored = %q", cfg.FastRoleIgnored, cfg.Ignored)
	}
	if w := FastRoleIgnoredWarning(cwd, cfg.FastRoleIgnored); !strings.Contains(w, "not used for the auto mode classifier") {
		t.Errorf("warning = %q", w)
	}

	writeJSONFile(t, filepath.Join(home, ".claude", "settings.json"), `{"modelRoles": {"fast": "anthropic/claude-haiku-4-5"}}`)
	if got := LoadAutoMode(cwd, LoadOptions{}).FastRole; got != "anthropic/claude-haiku-4-5" {
		t.Errorf("user fast role = %q", got)
	}
	extra := filepath.Join(t.TempDir(), "flag.json")
	writeJSONFile(t, extra, `{"modelRoles": {"fast": "faux/faux-2"}}`)
	if got := LoadAutoMode(cwd, LoadOptions{Extra: extra}).FastRole; got != "faux/faux-2" {
		t.Errorf("--settings fast role = %q, want it to win", got)
	}
}

func TestLoadAutoMode_SettingSourcesExcludeUser(t *testing.T) {
	cwd, _ := autoModeFixture(t)
	cfg := LoadAutoMode(cwd, LoadOptions{Sources: []paths.Scope{paths.ScopeProject}})
	if cfg.Allow != nil {
		t.Errorf("Allow = %q, want nil with user settings excluded", cfg.Allow)
	}
}

func TestLoadAutoMode_UnsetIsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := LoadAutoMode(t.TempDir(), LoadOptions{})
	if cfg.Environment != nil || cfg.Allow != nil || cfg.SoftDeny != nil || cfg.HardDeny != nil {
		t.Errorf("cfg = %+v, want every list unset", cfg)
	}
}

func TestSpliceAutoModeDefaults(t *testing.T) {
	defaults := []string{"d1", "d2"}
	cases := []struct {
		name string
		list []string
		want []string
	}{
		{"unset gives defaults", nil, []string{"d1", "d2"}},
		{"empty replaces", []string{}, []string{}},
		{"no marker replaces", []string{"mine"}, []string{"mine"}},
		{"marker splices in place", []string{"before", "$defaults", "after"}, []string{"before", "d1", "d2", "after"}},
		{"second marker dropped", []string{"$defaults", "x", "$defaults"}, []string{"d1", "d2", "x"}},
	}
	for _, c := range cases {
		if got := SpliceAutoModeDefaults(c.list, defaults); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
