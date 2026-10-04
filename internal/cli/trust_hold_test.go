package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
)

// printBlocked runs bashCallScript ("echo hi | tee hi.txt") under -p in
// the current scratch project and returns the run's blocked list and
// stderr.
func printBlocked(t *testing.T, args Args) ([]string, string) {
	t.Helper()
	args.Print = true
	args.PrintPrompt = "run a command"
	args.OutputFormat = "json"
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("exit %d; stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var parsed struct {
		Blocked []string `json:"blocked"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("parse json output: %v\n%s", err, stdout.String())
	}
	return parsed.Blocked, stderr.String()
}

// A -p run never shows the trust dialog, and Claude Code does not apply a
// project's allow rules there until the folder has been trusted: it says
// so on stderr. The same rule applies once the folder is trusted.
func TestRun_Print_UntrustedProjectAllowHeld(t *testing.T) {
	startFaux(t, bashCallScript)
	proj := scratchProject(t)
	t.Setenv("HARNESS_TRUST_ALL", "")
	writeProjectSettings(t, proj, `{"permissions":{"allow":["Bash(tee *)"]}}`)

	blocked, stderr := printBlocked(t, baseArgs())
	if len(blocked) == 0 {
		t.Errorf("untrusted: the project's allow rule let the command run")
	}
	if _, err := os.Stat(filepath.Join(proj, "hi.txt")); err == nil {
		t.Errorf("untrusted: hi.txt was written")
	}
	want := "Ignoring 1 permissions.allow rule from " + filepath.Join(".claude", "settings.json") + ": this folder has not been trusted. Run kiln here interactively once and trust it."
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}

	t.Setenv("HARNESS_TRUST_ALL", "1")
	if blocked, _ := printBlocked(t, baseArgs()); len(blocked) != 0 {
		t.Errorf("trusted: blocked = %v, want the allow rule to apply", blocked)
	}
}

// Deny rules from an untrusted project apply at once: they only restrict.
func TestRun_Print_UntrustedProjectDenyApplies(t *testing.T) {
	startFaux(t, bashCallScript)
	proj := scratchProject(t)
	t.Setenv("HARNESS_TRUST_ALL", "")
	writeProjectSettings(t, proj, `{"permissions":{"deny":["Bash(tee *)"]}}`)
	args := baseArgs()
	args.AllowedTools = []string{"Bash(tee *)"}
	blocked, _ := printBlocked(t, args)
	if len(blocked) == 0 || !strings.Contains(strings.Join(blocked, "\n"), "blocked by permission rules") {
		t.Errorf("blocked = %v, want the project's deny rule", blocked)
	}
}

// A repository cannot start a session in bypassPermissions: Claude Code
// ignores that defaultMode (and auto) from project and local settings.
func TestRun_Print_ProjectBypassModeIgnored(t *testing.T) {
	startFaux(t, bashCallScript)
	proj := scratchProject(t)
	t.Setenv("HARNESS_TRUST_ALL", "1")
	writeProjectSettings(t, proj, `{"permissions":{"defaultMode":"bypassPermissions"}}`)
	blocked, stderr := printBlocked(t, baseArgs())
	if len(blocked) == 0 {
		t.Errorf("the project's bypassPermissions mode let the command run")
	}
	if !strings.Contains(stderr, "Ignoring permissions.defaultMode in "+filepath.Join(".claude", "settings.json")) {
		t.Errorf("stderr = %q, want the ignored mode named", stderr)
	}
}

// A -p run runs hooks in an untrusted folder, as Claude Code does (trust
// is implied there), unless --setting-sources leaves the project's
// settings out: then its hooks are not read at all.
func TestRun_Print_ProjectHooksFollowSettingSources(t *testing.T) {
	startFaux(t, bashCallScript)
	proj := scratchProject(t)
	t.Setenv("HARNESS_TRUST_ALL", "")
	marker := filepath.Join(proj, "session-started")
	writeProjectSettings(t, proj, `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"touch `+marker+`"}]}]}}`)

	args := baseArgs()
	args.SettingSources = []string{"user"}
	printBlocked(t, args)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("--setting-sources user: the project's SessionStart hook ran")
	}

	printBlocked(t, baseArgs())
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("-p: the project's SessionStart hook did not run: %v", err)
	}
}

// The same holds for a plugin the person installed but only the project
// enables: with --setting-sources user it is not loaded, so its hooks do
// not run.
func TestRun_Print_ProjectEnabledPluginFollowsSettingSources(t *testing.T) {
	startFaux(t, bashCallScript)
	proj := scratchProject(t)
	t.Setenv("HARNESS_TRUST_ALL", "")
	home := os.Getenv("HOME")
	marker := filepath.Join(proj, "plugin-hook-ran")
	root := filepath.Join(home, "plugins", "demo")
	for path, body := range map[string]string{
		filepath.Join(home, ".claude", "plugins", "installed_plugins.json"): `{"version":2,"plugins":{"demo@market":[{"scope":"user","installPath":"` + root + `","version":"1.0.0"}]}}`,
		filepath.Join(root, ".claude-plugin", "plugin.json"):                `{"name":"demo","version":"1.0.0"}`,
		filepath.Join(root, "hooks", "hooks.json"):                          `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"touch ` + marker + `"}]}]}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeProjectSettings(t, proj, `{"enabledPlugins":{"demo@market":true}}`)

	args := baseArgs()
	args.SettingSources = []string{"user"}
	printBlocked(t, args)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("--setting-sources user: the hook of a plugin only the project enables ran")
	}
	printBlocked(t, baseArgs())
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("-p, every source: the plugin's hook did not run: %v", err)
	}
}

func TestTrustedHooks_HeldUntilEnabled(t *testing.T) {
	cfg := claudehooks.Config{claudehooks.SessionStart: {{Hooks: []claudehooks.Command{{Type: "command", Command: "true"}}}}}
	h := newTrustedHooks(cfg, false)
	if h.get() != nil {
		t.Fatal("hooks active before trust")
	}
	h.enable()
	if len(h.get()[claudehooks.SessionStart]) != 1 {
		t.Fatal("hooks not active after trust")
	}
	if newTrustedHooks(cfg, true).get() == nil {
		t.Fatal("hooks held in a trusted folder")
	}
}

func TestHeldRulesWarning(t *testing.T) {
	cwd := t.TempDir()
	s := claudesettings.Settings{
		HeldAllow: []string{"Bash(curl *)", "Read"},
		HeldFrom: []claudesettings.RuleSource{
			{File: filepath.Join(cwd, ".claude", "settings.json")},
			{File: filepath.Join(cwd, ".claude", "settings.local.json")},
		},
	}
	got := heldRulesWarning(cwd, s, false)
	want := "Ignoring 2 permissions.allow rules from " + filepath.Join(".claude", "settings.json") + ", " +
		filepath.Join(".claude", "settings.local.json") + ": this folder has not been trusted yet; they apply once you trust it."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if heldRulesWarning(cwd, claudesettings.Settings{}, true) != "" {
		t.Error("a warning with nothing held")
	}
}
