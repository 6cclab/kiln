package sandbox

import (
	"strings"

	"github.com/andrepato/harness/internal/claude/settings"
)

// Rule is a filesystem path a profile allows or denies: Path and
// everything under it when Segs is empty, otherwise the gitignore-style
// segments Segs ("**", "*", "?", "[…]") matched under Path.
type Rule struct {
	Path string
	Segs []string
}

func (r Rule) glob() bool { return len(r.Segs) > 0 }

// Config is the resolved sandbox configuration for a session: the merged
// "sandbox" settings plus what permission rules add to it.
type Config struct {
	Enabled           bool
	FailIfUnavailable bool
	AutoAllow         bool
	AllowUnsandboxed  bool
	Excluded          []string

	FilesystemDisabled bool
	AllowWrite         []Rule
	DenyWrite          []Rule
	DenyRead           []Rule
	AllowRead          []Rule

	AllowedDomains  []string
	DeniedDomains   []string
	StrictAllowlist bool

	AllowLocalBinding   bool
	AllowAllUnixSockets bool
	UnixSockets         []string
	MachLookup          []string
	HTTPProxyPort       int
	SOCKSProxyPort      int

	WeakerNested  bool
	WeakerNetwork bool
	AppleEvents   bool

	// DenyEnv are environment variables removed from sandboxed commands
	// (sandbox.credentials.envVars).
	DenyEnv []string

	// Notes are configuration kiln accepted but does not enforce the way
	// Claude Code does, for doctor.
	Notes []string
}

// FromSettings resolves the session's sandbox configuration from merged
// settings and the permission rules in force (perms: settings plus
// --allowed-tools/--disallowed-tools). cwd anchors relative rules.
//
// Claude Code merges permission rules into the sandbox
// (code.claude.com/docs/en/sandboxing, "Permission rules"): Edit allow
// rules into allowWrite, Edit deny rules into denyWrite, Read deny rules
// into denyRead, WebFetch(domain:…) allow and deny rules into the domain
// lists. credentials.files entries become read denials. A "mask" entry —
// which Claude Code serves through a TLS-terminating proxy kiln does not
// have — is enforced as "deny", the way Claude Code itself treats mask
// files on macOS.
func FromSettings(s settings.Settings, perms settings.Permissions, cwd string) Config {
	sb := s.Sandbox
	c := Config{
		Enabled:            sb.IsEnabled(),
		FailIfUnavailable:  sb.FailClosed(),
		AutoAllow:          sb.AutoAllow(),
		AllowUnsandboxed:   sb.UnsandboxedAllowed(),
		Excluded:           append([]string(nil), sb.ExcludedCommands...),
		FilesystemDisabled: sb.FilesystemDisabled(),
		StrictAllowlist:    sb.Network.StrictAllowlist != nil && *sb.Network.StrictAllowlist,
		AllowLocalBinding:  sb.Network.AllowLocalBinding != nil && *sb.Network.AllowLocalBinding,
		AllowAllUnixSockets: sb.Network.AllowAllUnixSockets != nil &&
			*sb.Network.AllowAllUnixSockets,
		MachLookup:    append([]string(nil), sb.Network.AllowMachLookup...),
		WeakerNested:  sb.EnableWeakerNestedSandbox != nil && *sb.EnableWeakerNestedSandbox,
		WeakerNetwork: sb.EnableWeakerNetworkIsolation != nil && *sb.EnableWeakerNetworkIsolation,
		AppleEvents:   sb.AllowAppleEvents != nil && *sb.AllowAppleEvents,
	}
	if sb.Network.HTTPProxyPort != nil {
		c.HTTPProxyPort = *sb.Network.HTTPProxyPort
	}
	if sb.Network.SOCKSProxyPort != nil {
		c.SOCKSProxyPort = *sb.Network.SOCKSProxyPort
	}
	for _, p := range sb.Network.AllowUnixSockets {
		c.UnixSockets = append(c.UnixSockets, p.Path)
	}

	fromPaths := func(ps []settings.SandboxPath) []Rule {
		var out []Rule
		for _, p := range ps {
			out = append(out, pathRule(p.Path))
		}
		return out
	}
	fromRules := func(rs []settings.SandboxRulePath) []Rule {
		var out []Rule
		for _, r := range rs {
			out = append(out, Rule{Path: r.Base, Segs: r.Segs})
		}
		return out
	}
	aw, dw, dr := settings.SandboxRulePaths(perms, cwd)
	c.AllowWrite = append(fromPaths(sb.Filesystem.AllowWrite), fromRules(aw)...)
	c.DenyWrite = append(fromPaths(sb.Filesystem.DenyWrite), fromRules(dw)...)
	c.DenyRead = append(fromPaths(sb.Filesystem.DenyRead), fromRules(dr)...)
	c.AllowRead = fromPaths(sb.Filesystem.AllowRead)
	for _, f := range sb.CredentialFiles {
		c.DenyRead = append(c.DenyRead, pathRule(f.Path))
		if f.Mode == "mask" {
			c.Notes = append(c.Notes, "credentials.files mask entry "+f.Path+" is enforced as deny (kiln has no TLS-terminating proxy)")
		}
	}
	seenEnv := map[string]bool{}
	for _, e := range sb.CredentialEnv {
		if !seenEnv[e.Name] {
			seenEnv[e.Name] = true
			c.DenyEnv = append(c.DenyEnv, e.Name)
		}
		if e.Mode == "mask" {
			c.Notes = append(c.Notes, "credentials.envVars mask entry "+e.Name+" is enforced as deny (kiln has no TLS-terminating proxy)")
		}
	}

	allowRules, denyRules := settings.SandboxRuleDomains(perms)
	c.AllowedDomains = append(append([]string(nil), sb.Network.AllowedDomains...), allowRules...)
	c.DeniedDomains = append(append([]string(nil), sb.Network.DeniedDomains...), denyRules...)
	if len(sb.IgnoreViolations) > 0 {
		c.Notes = append(c.Notes, "ignoreViolations is accepted but has no effect: kiln does not report individual violations")
	}
	return c
}

// pathRule turns a sandbox settings path into a Rule: a path with a
// wildcard is split at its first wildcard segment.
func pathRule(p string) Rule {
	if !strings.ContainsAny(p, "*?[") {
		return Rule{Path: p}
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.ContainsAny(s, "*?[") {
			base := strings.Join(segs[:i], "/")
			if base == "" {
				base = "/"
			}
			return Rule{Path: base, Segs: segs[i:]}
		}
	}
	return Rule{Path: p}
}
