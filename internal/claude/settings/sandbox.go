package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrepato/harness/internal/claude/paths"
)

// Claude Code's "sandbox" settings object, read and merged the way Claude
// Code documents it (code.claude.com/docs/en/settings-reference, "Sandbox
// settings", and code.claude.com/docs/en/sandboxing):
//
//   - Booleans: a later scope (local over project over user, --settings
//     last) wins, except where a key is honoured only from trusted sources.
//   - Arrays (excludedCommands, the filesystem path lists, the domain
//     lists, credentials): entries from every scope combine.
//   - filesystem.disabled, allowAppleEvents, network.strictAllowlist and
//     credentials "mask" entries are honoured only from user settings and
//     --settings; a repository's .claude files cannot set them.
//   - allowUnsandboxedCommands: a false from user settings or --settings
//     holds even when a project sets true.
//
// kiln has no managed (enterprise) settings tier, so none of the
// managed-only keys (allowManagedDomainsOnly, allowManagedReadPathsOnly,
// bwrapPath, socatPath) and no "admin-required" lock applies.
//
// Unknown keys are ignored. A key of the wrong type is skipped with a
// warning (Settings.SandboxWarnings) rather than discarding the file.

// SandboxPath is one entry of a sandbox path list, resolved to an absolute
// path by its prefix (Claude Code's "Sandbox path prefixes": "/" and "//"
// absolute, "~/" the home directory, "./" or no prefix the project root for
// project settings and the settings file's directory for user settings).
// A trailing "/" or "/**" is removed.
type SandboxPath struct {
	Path string
	// Glob is set when Path still contains a wildcard (*, ? or [).
	Glob bool
	// Trusted marks an entry from user settings or --settings.
	Trusted bool
}

// SandboxCredential is one sandbox.credentials entry: a file path or an
// environment variable name, and its mode ("deny" or "mask").
type SandboxCredential struct {
	Path    string // files only, resolved like SandboxPath
	Name    string // envVars only
	Mode    string
	Trusted bool
}

// Sandbox is the merged "sandbox" settings object.
type Sandbox struct {
	Enabled                      *bool
	FailIfUnavailable            *bool
	AutoAllowBashIfSandboxed     *bool
	AllowUnsandboxedCommands     *bool
	EnableWeakerNestedSandbox    *bool
	EnableWeakerNetworkIsolation *bool
	AllowAppleEvents             *bool
	ExcludedCommands             []string
	IgnoreViolations             map[string][]string

	Filesystem SandboxFilesystem
	Network    SandboxNetwork
	// CredentialFiles and CredentialEnv are sandbox.credentials.files and
	// .envVars, in merge order.
	CredentialFiles []SandboxCredential
	CredentialEnv   []SandboxCredential

	// set records whether any loaded file had a sandbox object at all.
	set bool
	// unsandboxedHeld is a false allowUnsandboxedCommands from a trusted
	// source, which a project's true does not override.
	unsandboxedHeld bool
}

// SandboxFilesystem is sandbox.filesystem.
type SandboxFilesystem struct {
	AllowWrite []SandboxPath
	DenyWrite  []SandboxPath
	DenyRead   []SandboxPath
	AllowRead  []SandboxPath
	Disabled   *bool
}

// SandboxNetwork is sandbox.network.
type SandboxNetwork struct {
	AllowedDomains      []string
	DeniedDomains       []string
	AllowUnixSockets    []SandboxPath
	AllowMachLookup     []string
	AllowAllUnixSockets *bool
	AllowLocalBinding   *bool
	StrictAllowlist     *bool
	HTTPProxyPort       *int
	SOCKSProxyPort      *int
}

// Configured reports whether any loaded settings file had a sandbox
// object.
func (s Sandbox) Configured() bool { return s.set }

// IsEnabled is sandbox.enabled; unset means off (Claude Code's default).
func (s Sandbox) IsEnabled() bool { return s.Enabled != nil && *s.Enabled }

// AutoAllow is sandbox.autoAllowBashIfSandboxed; unset means true.
func (s Sandbox) AutoAllow() bool {
	return s.AutoAllowBashIfSandboxed == nil || *s.AutoAllowBashIfSandboxed
}

// UnsandboxedAllowed is sandbox.allowUnsandboxedCommands; unset means true.
func (s Sandbox) UnsandboxedAllowed() bool {
	return s.AllowUnsandboxedCommands == nil || *s.AllowUnsandboxedCommands
}

// FailClosed is sandbox.failIfUnavailable; unset means false.
func (s Sandbox) FailClosed() bool { return s.FailIfUnavailable != nil && *s.FailIfUnavailable }

func isTrue(b *bool) bool { return b != nil && *b }

// FilesystemDisabled is sandbox.filesystem.disabled.
func (s Sandbox) FilesystemDisabled() bool { return isTrue(s.Filesystem.Disabled) }

// sandboxSource describes the settings file one sandbox object came from.
type sandboxSource struct {
	file string
	// trusted: user settings or --settings (Claude Code's "User or
	// managed" / CLI scope); false for a repository's .claude files and
	// <cwd>/.kiln/settings.local.json.
	trusted bool
	// held: a repository-supplied .kiln/settings.local.json in an
	// untrusted folder. Only the entries that narrow the sandbox apply.
	held bool
	// rel is the directory a "./" or prefix-less path resolves against.
	rel  string
	home string
}

// mergeSandbox folds the sandbox object of one settings file (its raw
// JSON, already known to parse) into merged.
func mergeSandbox(merged *Settings, data []byte, src sandboxSource) {
	var top struct {
		Sandbox json.RawMessage `json:"sandbox"`
	}
	if json.Unmarshal(data, &top) != nil || len(top.Sandbox) == 0 || string(top.Sandbox) == "null" {
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(top.Sandbox, &obj); err != nil {
		merged.SandboxWarnings = append(merged.SandboxWarnings, fmt.Sprintf("%s: sandbox is not an object; ignored", src.file))
		return
	}
	s := &merged.Sandbox
	s.set = true
	warn := func(key string) {
		merged.SandboxWarnings = append(merged.SandboxWarnings, fmt.Sprintf("%s: sandbox.%s has the wrong type; ignored", src.file, key))
	}
	boolKey := func(o map[string]json.RawMessage, prefix, key string) *bool {
		raw, ok := o[key]
		if !ok {
			return nil
		}
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			warn(prefix + key)
			return nil
		}
		return &b
	}
	strings_ := func(o map[string]json.RawMessage, prefix, key string) []string {
		raw, ok := o[key]
		if !ok {
			return nil
		}
		var v []string
		if err := json.Unmarshal(raw, &v); err != nil {
			warn(prefix + key)
			return nil
		}
		return v
	}
	paths := func(o map[string]json.RawMessage, prefix, key string) []SandboxPath {
		var out []SandboxPath
		for _, p := range strings_(o, prefix, key) {
			if r, ok := resolveSandboxPath(p, src); ok {
				out = append(out, r)
			}
		}
		return out
	}

	// Entries that only narrow the sandbox apply from every source, held
	// files included. Everything else is skipped for a held file.
	var fs, net, creds map[string]json.RawMessage
	sub := func(key string) map[string]json.RawMessage {
		raw, ok := obj[key]
		if !ok {
			return nil
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			warn(key)
			return nil
		}
		return m
	}
	fs, net, creds = sub("filesystem"), sub("network"), sub("credentials")

	s.Filesystem.DenyWrite = append(s.Filesystem.DenyWrite, paths(fs, "filesystem.", "denyWrite")...)
	s.Filesystem.DenyRead = append(s.Filesystem.DenyRead, paths(fs, "filesystem.", "denyRead")...)
	s.Network.DeniedDomains = append(s.Network.DeniedDomains, strings_(net, "network.", "deniedDomains")...)
	mergeCredentials(merged, creds, src)
	if v := boolKey(net, "network.", "strictAllowlist"); v != nil && src.trusted {
		// "When any of the honored sources sets it to true, it stays on."
		if *v || s.Network.StrictAllowlist == nil {
			s.Network.StrictAllowlist = v
		}
	}
	if src.held {
		return
	}

	if v := boolKey(obj, "", "enabled"); v != nil {
		s.Enabled = v
	}
	if v := boolKey(obj, "", "failIfUnavailable"); v != nil {
		s.FailIfUnavailable = v
	}
	if v := boolKey(obj, "", "autoAllowBashIfSandboxed"); v != nil {
		s.AutoAllowBashIfSandboxed = v
	}
	if v := boolKey(obj, "", "allowUnsandboxedCommands"); v != nil {
		switch {
		case !*v && src.trusted:
			s.AllowUnsandboxedCommands = v
			s.unsandboxedHeld = true
		case *v && s.unsandboxedHeld && !src.trusted:
			// A project's true does not override a user's false.
		default:
			s.AllowUnsandboxedCommands = v
			if src.trusted {
				s.unsandboxedHeld = false
			}
		}
	}
	if v := boolKey(obj, "", "enableWeakerNestedSandbox"); v != nil {
		s.EnableWeakerNestedSandbox = v
	}
	if v := boolKey(obj, "", "enableWeakerNetworkIsolation"); v != nil {
		s.EnableWeakerNetworkIsolation = v
	}
	if v := boolKey(obj, "", "allowAppleEvents"); v != nil && src.trusted {
		s.AllowAppleEvents = v
	}
	s.ExcludedCommands = append(s.ExcludedCommands, strings_(obj, "", "excludedCommands")...)
	if raw, ok := obj["ignoreViolations"]; ok {
		var m map[string][]string
		if err := json.Unmarshal(raw, &m); err != nil {
			warn("ignoreViolations")
		} else {
			if s.IgnoreViolations == nil {
				s.IgnoreViolations = map[string][]string{}
			}
			for k, v := range m {
				s.IgnoreViolations[k] = append(s.IgnoreViolations[k], v...)
			}
		}
	}

	s.Filesystem.AllowWrite = append(s.Filesystem.AllowWrite, paths(fs, "filesystem.", "allowWrite")...)
	s.Filesystem.AllowRead = append(s.Filesystem.AllowRead, paths(fs, "filesystem.", "allowRead")...)
	if v := boolKey(fs, "filesystem.", "disabled"); v != nil && src.trusted {
		s.Filesystem.Disabled = v
	}

	s.Network.AllowedDomains = append(s.Network.AllowedDomains, strings_(net, "network.", "allowedDomains")...)
	s.Network.AllowUnixSockets = append(s.Network.AllowUnixSockets, paths(net, "network.", "allowUnixSockets")...)
	s.Network.AllowMachLookup = append(s.Network.AllowMachLookup, strings_(net, "network.", "allowMachLookup")...)
	if v := boolKey(net, "network.", "allowAllUnixSockets"); v != nil {
		s.Network.AllowAllUnixSockets = v
	}
	if v := boolKey(net, "network.", "allowLocalBinding"); v != nil {
		s.Network.AllowLocalBinding = v
	}
	port := func(key string) *int {
		raw, ok := net[key]
		if !ok {
			return nil
		}
		var n int
		if err := json.Unmarshal(raw, &n); err != nil || n <= 0 || n > 65535 {
			warn("network." + key)
			return nil
		}
		return &n
	}
	if p := port("httpProxyPort"); p != nil {
		s.Network.HTTPProxyPort = p
	}
	if p := port("socksProxyPort"); p != nil {
		s.Network.SOCKSProxyPort = p
	}
}

// mergeCredentials folds sandbox.credentials.files and .envVars. "mask"
// entries are honoured only from trusted sources (Claude Code drops them
// from a repository's settings); "deny" entries from every source.
func mergeCredentials(merged *Settings, creds map[string]json.RawMessage, src sandboxSource) {
	if creds == nil {
		return
	}
	type entry struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Mode string `json:"mode"`
	}
	read := func(key string) []entry {
		raw, ok := creds[key]
		if !ok {
			return nil
		}
		var v []entry
		if err := json.Unmarshal(raw, &v); err != nil {
			merged.SandboxWarnings = append(merged.SandboxWarnings, fmt.Sprintf("%s: sandbox.credentials.%s has the wrong type; ignored", src.file, key))
			return nil
		}
		return v
	}
	keep := func(mode string) bool {
		return mode == "deny" || (mode == "mask" && src.trusted && !src.held)
	}
	for _, e := range read("files") {
		if !keep(e.Mode) || e.Path == "" {
			continue
		}
		if p, ok := resolveSandboxPath(e.Path, src); ok {
			merged.Sandbox.CredentialFiles = append(merged.Sandbox.CredentialFiles,
				SandboxCredential{Path: p.Path, Mode: e.Mode, Trusted: src.trusted})
		}
	}
	for _, e := range read("envVars") {
		if !keep(e.Mode) || !validEnvName(e.Name) {
			continue
		}
		merged.Sandbox.CredentialEnv = append(merged.Sandbox.CredentialEnv,
			SandboxCredential{Name: e.Name, Mode: e.Mode, Trusted: src.trusted})
	}
}

// validEnvName: a letter or underscore, then letters, digits and
// underscores (Claude Code's rule for credentials.envVars names).
func validEnvName(n string) bool {
	if n == "" {
		return false
	}
	for i, r := range n {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// resolveSandboxPath applies Claude Code's sandbox path prefixes.
func resolveSandboxPath(p string, src sandboxSource) (SandboxPath, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return SandboxPath{}, false
	}
	var abs string
	switch {
	case strings.HasPrefix(p, "//"):
		abs = "/" + strings.TrimLeft(p, "/")
	case strings.HasPrefix(p, "/"):
		abs = p
	case p == "~" || strings.HasPrefix(p, "~/"):
		if src.home == "" {
			return SandboxPath{}, false
		}
		abs = filepath.Join(src.home, strings.TrimPrefix(p, "~"))
	default:
		if src.rel == "" {
			return SandboxPath{}, false
		}
		abs = filepath.Join(src.rel, p)
	}
	for {
		trimmed := strings.TrimSuffix(strings.TrimSuffix(abs, "/**"), "/")
		if trimmed == abs || trimmed == "" {
			break
		}
		abs = trimmed
	}
	abs = filepath.Clean(abs)
	return SandboxPath{Path: abs, Glob: strings.ContainsAny(abs, "*?["), Trusted: src.trusted}, true
}

// sandboxSourceFor describes a settings file for mergeSandbox: user
// settings and --settings are trusted; "./" paths resolve against the
// project root (cwd) for project and local settings and against the
// file's own directory otherwise.
func sandboxSourceFor(cwd, file string, scope paths.Scope, cli, held bool) sandboxSource {
	src := sandboxSource{file: file, held: held, home: sandboxHome()}
	switch {
	case cli:
		src.trusted, src.rel = true, filepath.Dir(file)
		if abs, err := filepath.Abs(src.rel); err == nil {
			src.rel = abs
		}
	case scope == paths.ScopeUser:
		src.trusted, src.rel = true, filepath.Dir(file)
	default:
		src.rel = cwd
		if abs, err := filepath.Abs(cwd); err == nil {
			src.rel = abs
		}
	}
	return src
}

// sandboxHome is the home directory sandbox "~/" paths resolve against.
func sandboxHome() string {
	h, _ := os.UserHomeDir()
	return h
}
