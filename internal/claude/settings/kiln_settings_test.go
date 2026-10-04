package settings

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadSettingsKilnFiles: kiln's own files are read with Claude Code's.
// ~/.kiln/settings.json adds to the user scope and wins over
// ~/.claude/settings.json for a single value; .kiln/settings.local.json
// adds to the local scope and wins over .claude/settings.local.json. Each
// rule keeps its file as its source, with user rules anchored at ~/.kiln.
func TestLoadSettingsKilnFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	put(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"cc/user","effortLevel":"low","permissions":{"allow":["Read"]}}`)
	put(t, filepath.Join(home, ".kiln", "settings.json"), `{"model":"kiln/user","permissions":{"deny":["Read(/secret/**)"]}}`)
	put(t, filepath.Join(cwd, ".claude", "settings.local.json"), `{"model":"cc/local","permissions":{"allow":["Bash(npm test *)"]}}`)

	s := LoadSettings(cwd, LoadOptions{Trusted: true})
	if s.Model != "cc/local" {
		t.Errorf("model = %q, want cc/local (local beats user)", s.Model)
	}
	if s.EffortLevel != "low" {
		t.Errorf("effortLevel = %q, want low (kept from ~/.claude)", s.EffortLevel)
	}

	put(t, filepath.Join(cwd, ".kiln", "settings.local.json"), `{"model":"kiln/local","permissions":{"allow":["Bash(make build *)"],"deny":["Bash(npm test *)"]}}`)
	s = LoadSettings(cwd, LoadOptions{Trusted: true})
	if s.Model != "kiln/local" {
		t.Errorf("model = %q, want kiln/local", s.Model)
	}
	os.Remove(filepath.Join(cwd, ".kiln", "settings.local.json"))
	os.Remove(filepath.Join(cwd, ".claude", "settings.local.json"))
	if s := LoadSettings(cwd, LoadOptions{}); s.Model != "kiln/user" {
		t.Errorf("model = %q, want kiln/user (kiln's user file wins over Claude Code's)", s.Model)
	}

	put(t, filepath.Join(cwd, ".claude", "settings.local.json"), `{"permissions":{"allow":["Bash(npm test *)"]}}`)
	put(t, filepath.Join(cwd, ".kiln", "settings.local.json"), `{"permissions":{"allow":["Bash(make build *)"],"deny":["Bash(npm test *)"]}}`)
	s = LoadSettings(cwd, LoadOptions{Trusted: true})
	kilnUser := RuleSource{Scope: paths.ScopeUser, File: filepath.Join(home, ".kiln", "settings.json"), Root: filepath.Join(home, ".kiln")}
	kilnLocal := RuleSource{Scope: paths.ScopeLocal, File: filepath.Join(cwd, ".kiln", "settings.local.json")}
	if i := indexOf(s.Permissions.Deny, "Read(/secret/**)"); i < 0 || s.Permissions.DenyFrom[i] != kilnUser {
		t.Errorf("kiln user deny rule: index %d, sources %+v", i, s.Permissions.DenyFrom)
	}
	if i := indexOf(s.Permissions.Allow, "Bash(make build *)"); i < 0 || s.Permissions.AllowFrom[i] != kilnLocal {
		t.Errorf("kiln local allow rule: index %d, sources %+v", i, s.Permissions.AllowFrom)
	}

	// Honoured: the kiln allow rule allows; its deny beats Claude Code's allow.
	if got := DecideIn(s.Permissions, cwd, "bash", "make build", ModeManual); got != Allow {
		t.Errorf("make build = %v, want allow (kiln local rule)", got)
	}
	if got := DecideIn(s.Permissions, cwd, "bash", "npm test", ModeManual); got != Deny {
		t.Errorf("npm test = %v, want deny (kiln deny beats .claude allow)", got)
	}
	// A /path rule in ~/.kiln/settings.json anchors at ~/.kiln.
	secret := filepath.Join(home, ".kiln", "secret", "k")
	if got := DecideIn(s.Permissions, cwd, "read", secret, ModeManual); got != Deny {
		t.Errorf("read %s = %v, want deny (anchored at ~/.kiln)", secret, got)
	}
}

// TestLoadSettingsLoadedFrom_DistinguishesKilnFromClaudeCode checks
// LoadedFrom tells ~/.claude/settings.json and ~/.kiln/settings.json
// (both ScopeUser) apart, rather than recording the scope alone twice —
// the regression for qa/findings/20261004T205021Z-doctor-misses-sandbox-
// and-hook-events.json: /doctor used to print "settings   user, user,
// project", indistinguishable.
func TestLoadSettingsLoadedFrom_DistinguishesKilnFromClaudeCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	put(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"cc/user"}`)
	put(t, filepath.Join(home, ".kiln", "settings.json"), `{}`)
	put(t, filepath.Join(cwd, ".claude", "settings.json"), `{}`)

	s := LoadSettings(cwd, LoadOptions{})
	labels := make([]string, len(s.LoadedFrom))
	for i, f := range s.LoadedFrom {
		labels[i] = f.Label()
	}
	want := []string{"~/.claude", "~/.kiln", ".claude"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("labels[%d] = %q, want %q (full: %v)", i, labels[i], want[i], labels)
		}
	}
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

// TestLoadSettingsKilnLocalHeldUntilTrusted: a .kiln/settings.local.json
// that came with the repository (tracked in git, or reached through a
// symlinked .kiln) has its allow rules held until the folder is trusted;
// its deny and ask rules still apply. Under -p, git decides; before an
// interactive trust dialog every one is held (TestLoadSettings_
// UntrustedKilnLocalInteractive).
func TestLoadSettingsKilnLocalHeldUntilTrusted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	body := `{"model":"evil/model","permissions":{"allow":["Bash(curl *)"],"deny":["Bash(rm *)"]}}`

	tracked := t.TempDir()
	put(t, filepath.Join(tracked, ".kiln", "settings.local.json"), body)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-f", ".kiln/settings.local.json"}} {
		if out, err := exec.Command("git", append([]string{"-C", tracked}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	linked := t.TempDir()
	real := t.TempDir()
	put(t, filepath.Join(real, "settings.local.json"), body)
	if err := os.Symlink(real, filepath.Join(linked, ".kiln")); err != nil {
		t.Fatal(err)
	}

	untracked := t.TempDir()
	put(t, filepath.Join(untracked, ".kiln", "settings.local.json"), body)

	for _, c := range []struct {
		name string
		cwd  string
		held bool
	}{{"tracked", tracked, true}, {"symlinked", linked, true}, {"kiln's own", untracked, false}} {
		s := LoadSettings(c.cwd, LoadOptions{Headless: true})
		if held := indexOf(s.Permissions.Allow, "Bash(curl *)") < 0; held != c.held {
			t.Errorf("%s: allow held = %v, want %v", c.name, held, c.held)
		}
		if c.held {
			if indexOf(s.HeldAllow, "Bash(curl *)") < 0 || len(s.HeldFiles) == 0 || len(s.HeldFrom) != len(s.HeldAllow) {
				t.Errorf("%s: HeldAllow=%q HeldFiles=%q", c.name, s.HeldAllow, s.HeldFiles)
			}
			if s.Model == "evil/model" {
				t.Errorf("%s: model applied from an untrusted file", c.name)
			}
		}
		if indexOf(s.Permissions.Deny, "Bash(rm *)") < 0 {
			t.Errorf("%s: deny rule not applied", c.name)
		}
		if s := LoadSettings(c.cwd, LoadOptions{Trusted: true}); indexOf(s.Permissions.Allow, "Bash(curl *)") < 0 || len(s.HeldFiles) != 0 {
			t.Errorf("%s, trusted: allow rule not applied", c.name)
		}
	}
}
