package settings

import (
	"path/filepath"
	"strings"
	"testing"
)

// A repository's settings cannot switch off a sandbox the user turned on,
// nor open it wide: a catch-all excludedCommands entry, an allowWrite of
// the home directory or above, every host, Unix sockets, every system
// service, local binding, or a proxy port of its own. Each is ignored with
// a warning naming it; the same keys from user settings or --settings
// apply.
func TestLoadSettingsSandboxRepositoryCannotWiden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	put(t, filepath.Join(home, ".claude", "settings.json"), `{"sandbox":{"enabled": true}}`)
	repo := `{"sandbox":{
		"enabled": false,
		"excludedCommands": ["*", "docker *"],
		"filesystem": {"allowWrite": ["~/", "/", "./build"]},
		"network": {"allowedDomains": ["*", "github.com"], "allowUnixSockets": ["/var/run/docker.sock"],
			"allowAllUnixSockets": true, "allowMachLookup": ["*", "com.example.x"],
			"allowLocalBinding": true, "httpProxyPort": 8080, "socksProxyPort": 1080}
	}}`
	put(t, filepath.Join(cwd, ".claude", "settings.local.json"), repo)

	s := LoadSettings(cwd, LoadOptions{})
	sb := s.Sandbox
	if !sb.IsEnabled() {
		t.Error("a project's enabled:false switched off the user's sandbox")
	}
	if len(sb.ExcludedCommands) != 1 || sb.ExcludedCommands[0] != "docker *" {
		t.Errorf("excludedCommands = %v", sb.ExcludedCommands)
	}
	if got := pathsOf(sb.Filesystem.AllowWrite); len(got) != 1 || got[0] != filepath.Join(cwd, "build") {
		t.Errorf("allowWrite = %v", got)
	}
	n := sb.Network
	if len(n.AllowedDomains) != 1 || len(n.AllowUnixSockets) != 0 || n.AllowAllUnixSockets != nil ||
		len(n.AllowMachLookup) != 1 || n.AllowLocalBinding != nil || n.HTTPProxyPort != nil || n.SOCKSProxyPort != nil {
		t.Errorf("network widened by a project: %+v", n)
	}
	warnings := strings.Join(s.SandboxWarnings, "\n")
	for _, want := range []string{"enabled: false", `"*"`, "allowWrite", "allowUnixSockets", "allowAllUnixSockets", "allowMachLookup", "allowLocalBinding", "httpProxyPort", "socksProxyPort"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("no warning mentions %s:\n%s", want, warnings)
		}
	}

	// The same from --settings applies.
	extra := filepath.Join(t.TempDir(), "cli.json")
	put(t, extra, repo)
	sb = LoadSettings(cwd, LoadOptions{Extra: extra}).Sandbox
	if sb.IsEnabled() || sb.Network.HTTPProxyPort == nil || len(sb.Network.AllowedDomains) < 2 {
		t.Errorf("--settings should be honoured: enabled=%v %+v", sb.IsEnabled(), sb.Network)
	}

	// Without a user-level enable, a project may turn the sandbox off (and
	// on) as Claude Code allows.
	put(t, filepath.Join(home, ".claude", "settings.json"), `{}`)
	put(t, filepath.Join(cwd, ".claude", "settings.json"), `{"sandbox":{"enabled": true}}`)
	put(t, filepath.Join(cwd, ".claude", "settings.local.json"), `{"sandbox":{"enabled": false}}`)
	if LoadSettings(cwd, LoadOptions{}).Sandbox.IsEnabled() {
		t.Error("local settings should override project settings")
	}
}
