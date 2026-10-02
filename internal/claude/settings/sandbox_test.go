package settings

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLoadSettingsSandboxDefaults: with no sandbox object the sandbox is
// off, auto-allow and the unsandboxed retry default to true, and nothing
// is excluded (Claude Code's documented defaults).
func TestLoadSettingsSandboxDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := LoadSettings(t.TempDir(), LoadOptions{})
	sb := s.Sandbox
	if sb.Configured() || sb.IsEnabled() || !sb.AutoAllow() || !sb.UnsandboxedAllowed() || sb.FailClosed() || sb.FilesystemDisabled() {
		t.Fatalf("defaults wrong: %+v", sb)
	}
}

// TestLoadSettingsSandboxMerge: booleans take the later scope, arrays
// combine across scopes, and paths resolve by Claude Code's prefixes ("./"
// is the project root in project settings and the settings file's
// directory in user settings).
func TestLoadSettingsSandboxMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	put(t, filepath.Join(home, ".claude", "settings.json"), `{"sandbox":{
		"enabled": true, "autoAllowBashIfSandboxed": false,
		"excludedCommands": ["docker *"],
		"filesystem": {"allowWrite": ["~/.kube", "./out", "/tmp/build/**"], "denyRead": ["~/.aws/credentials/"]},
		"network": {"allowedDomains": ["github.com"], "deniedDomains": ["uploads.github.com"]},
		"someFutureKey": {"x": 1}
	}}`)
	put(t, filepath.Join(cwd, ".claude", "settings.json"), `{"sandbox":{
		"autoAllowBashIfSandboxed": true,
		"excludedCommands": ["gh *"],
		"filesystem": {"allowWrite": ["./out", "//abs/x"], "denyWrite": ["secrets"]},
		"network": {"allowedDomains": ["*.npmjs.org"], "allowLocalBinding": true, "httpProxyPort": 8080}
	}}`)

	sb := LoadSettings(cwd, LoadOptions{}).Sandbox
	if !sb.IsEnabled() {
		t.Error("enabled from user settings lost")
	}
	if !sb.AutoAllow() {
		t.Error("project's autoAllowBashIfSandboxed true should win over user's false")
	}
	if want := []string{"docker *", "gh *"}; !reflect.DeepEqual(sb.ExcludedCommands, want) {
		t.Errorf("excludedCommands = %v, want %v", sb.ExcludedCommands, want)
	}
	gotWrite := pathsOf(sb.Filesystem.AllowWrite)
	wantWrite := []string{
		filepath.Join(home, ".kube"),
		filepath.Join(home, ".claude", "out"), // "./" in user settings: under ~/.claude
		"/tmp/build",                          // trailing /** removed
		filepath.Join(cwd, "out"),             // "./" in project settings: the project root
		"/abs/x",                              // "//" is absolute too
	}
	if !reflect.DeepEqual(gotWrite, wantWrite) {
		t.Errorf("allowWrite = %v, want %v", gotWrite, wantWrite)
	}
	if got := pathsOf(sb.Filesystem.DenyRead); !reflect.DeepEqual(got, []string{filepath.Join(home, ".aws", "credentials")}) {
		t.Errorf("denyRead = %v (trailing slash should be stripped)", got)
	}
	if got := pathsOf(sb.Filesystem.DenyWrite); !reflect.DeepEqual(got, []string{filepath.Join(cwd, "secrets")}) {
		t.Errorf("denyWrite = %v", got)
	}
	if want := []string{"github.com", "*.npmjs.org"}; !reflect.DeepEqual(sb.Network.AllowedDomains, want) {
		t.Errorf("allowedDomains = %v, want %v", sb.Network.AllowedDomains, want)
	}
	if !isTrue(sb.Network.AllowLocalBinding) || sb.Network.HTTPProxyPort == nil || *sb.Network.HTTPProxyPort != 8080 {
		t.Errorf("network flags not merged: %+v", sb.Network)
	}
}

// TestLoadSettingsSandboxTrustedOnlyKeys: filesystem.disabled,
// allowAppleEvents, network.strictAllowlist and credential "mask" entries
// are honoured only from user settings and --settings; a repository's
// .claude files cannot set them.
func TestLoadSettingsSandboxTrustedOnlyKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	put(t, filepath.Join(cwd, ".claude", "settings.json"), `{"sandbox":{
		"enabled": true, "allowAppleEvents": true,
		"filesystem": {"disabled": true},
		"network": {"strictAllowlist": true},
		"credentials": {"envVars": [{"name":"GH_TOKEN","mode":"mask"},{"name":"NPM_TOKEN","mode":"deny"}]}
	}}`)
	sb := LoadSettings(cwd, LoadOptions{}).Sandbox
	if sb.FilesystemDisabled() || isTrue(sb.AllowAppleEvents) || isTrue(sb.Network.StrictAllowlist) {
		t.Errorf("a project set a user-only key: %+v", sb)
	}
	if len(sb.CredentialEnv) != 1 || sb.CredentialEnv[0].Name != "NPM_TOKEN" {
		t.Errorf("credentials = %+v, want only the project's deny entry", sb.CredentialEnv)
	}

	put(t, filepath.Join(home, ".claude", "settings.json"), `{"sandbox":{"allowAppleEvents": true, "filesystem": {"disabled": true}, "network": {"strictAllowlist": true}}}`)
	sb = LoadSettings(cwd, LoadOptions{}).Sandbox
	if !sb.FilesystemDisabled() || !isTrue(sb.AllowAppleEvents) || !isTrue(sb.Network.StrictAllowlist) {
		t.Errorf("user settings should set the user-only keys: %+v", sb)
	}

	extra := filepath.Join(t.TempDir(), "cli.json")
	put(t, extra, `{"sandbox":{"network": {"strictAllowlist": false}}}`)
	sb = LoadSettings(cwd, LoadOptions{Extra: extra}).Sandbox
	if !isTrue(sb.Network.StrictAllowlist) {
		t.Error("strictAllowlist: once a trusted source sets true it stays on")
	}
}

// TestLoadSettingsSandboxUnsandboxedHold: a false allowUnsandboxedCommands
// in user settings holds against a project's true; a project's false still
// applies over a user's true.
func TestLoadSettingsSandboxUnsandboxedHold(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	put(t, filepath.Join(home, ".claude", "settings.json"), `{"sandbox":{"allowUnsandboxedCommands": false}}`)
	put(t, filepath.Join(cwd, ".claude", "settings.json"), `{"sandbox":{"allowUnsandboxedCommands": true}}`)
	if LoadSettings(cwd, LoadOptions{}).Sandbox.UnsandboxedAllowed() {
		t.Error("project true overrode user false")
	}
	put(t, filepath.Join(home, ".claude", "settings.json"), `{"sandbox":{"allowUnsandboxedCommands": true}}`)
	put(t, filepath.Join(cwd, ".claude", "settings.json"), `{"sandbox":{"allowUnsandboxedCommands": false}}`)
	if LoadSettings(cwd, LoadOptions{}).Sandbox.UnsandboxedAllowed() {
		t.Error("project false should apply")
	}
}

// TestLoadSettingsSandboxBadTypes: a wrong-typed key is skipped with a
// warning, and the rest of the file (sandbox and otherwise) still applies.
func TestLoadSettingsSandboxBadTypes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	put(t, filepath.Join(cwd, ".claude", "settings.json"), `{"model":"a/b","sandbox":{
		"enabled": "yes", "excludedCommands": "docker", "autoAllowBashIfSandboxed": false,
		"network": {"httpProxyPort": 99999, "allowedDomains": ["ok.com"]}
	}}`)
	s := LoadSettings(cwd, LoadOptions{})
	if s.Model != "a/b" {
		t.Error("the rest of the file was dropped")
	}
	if s.Sandbox.IsEnabled() || s.Sandbox.AutoAllow() || len(s.Sandbox.ExcludedCommands) != 0 || s.Sandbox.Network.HTTPProxyPort != nil {
		t.Errorf("bad values applied: %+v", s.Sandbox)
	}
	if len(s.Sandbox.Network.AllowedDomains) != 1 {
		t.Error("a good sibling key was dropped")
	}
	joined := strings.Join(s.SandboxWarnings, "\n")
	for _, k := range []string{"sandbox.enabled", "sandbox.excludedCommands", "sandbox.network.httpProxyPort"} {
		if !strings.Contains(joined, k) {
			t.Errorf("no warning for %s in %q", k, joined)
		}
	}
}

// TestSandboxRulePaths: Edit allow rules feed allowWrite, Edit deny rules
// denyWrite and Read deny rules denyRead, anchored the way the file tools
// anchor them; WebFetch(domain:…) rules feed the domain lists.
func TestSandboxRulePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	p := Permissions{
		Allow: []string{"Edit(//opt/cache/**)", "Edit", "Read(src/**)", "WebFetch(domain:go.dev)"},
		Deny:  []string{"Read(~/.ssh/**)", "Edit(./Secrets/*.pem)", "Read(!keep)", "WebFetch(domain:evil.com)", "Bash(rm *)"},
	}
	aw, dw, dr := SandboxRulePaths(p, cwd)
	if len(aw) != 1 || aw[0].Base != "/opt/cache" || !reflect.DeepEqual(aw[0].Segs, []string{"**"}) {
		t.Errorf("allowWrite = %+v", aw)
	}
	if len(dw) != 1 || dw[0].Base != filepath.Join(cwd, "Secrets") || !reflect.DeepEqual(dw[0].Segs, []string{"*.pem"}) {
		t.Errorf("denyWrite = %+v (segments must keep their case)", dw)
	}
	if len(dr) != 1 || dr[0].Base != filepath.Join(home, ".ssh") {
		t.Errorf("denyRead = %+v", dr)
	}
	allow, deny := SandboxRuleDomains(p)
	if !reflect.DeepEqual(allow, []string{"go.dev"}) || !reflect.DeepEqual(deny, []string{"evil.com"}) {
		t.Errorf("domains = %v / %v", allow, deny)
	}
}

func pathsOf(ps []SandboxPath) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Path)
	}
	return out
}
