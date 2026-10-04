package cli

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/sandbox"
)

// startSandbox sets up the session's OS sandbox for the bash tools from
// the "sandbox" settings (Claude Code's sandboxing,
// code.claude.com/docs/en/sandboxing): it binds the sandbox to the
// permission gate (auto-allow, the unsandboxed retry, network approvals)
// and to env (what the bash tools run under).
//
// When sandbox.enabled is set but the sandbox cannot start (no
// sandbox-exec, no bubblewrap or socat, an unsupported platform), Claude
// Code runs commands unsandboxed unless sandbox.failIfUnavailable is set,
// in which case it refuses to start. kiln does the same, and says so with
// one startup warning: it never runs unsandboxed silently. The error
// return is that refusal.
//
// It returns nil when sandboxing is off. The caller closes the manager.
//
// settingsFiles are the settings files the session reads and reloads
// (claudesettings.SettingsFiles): no sandboxed command may write one, as
// named or where it resolves to (protectSettingsFiles).
func startSandbox(cwd string, s claudesettings.Settings, perms claudesettings.Permissions, settingsFiles []string, gate *permission.Gate, env *execenv.Env, warn func(string)) (*sandbox.Manager, error) {
	for _, w := range s.SandboxWarnings {
		warn(w)
	}
	if !s.Sandbox.IsEnabled() {
		return nil, nil
	}
	for _, w := range claudesettings.SandboxRuleWarnings(perms, cwd) {
		warn(w)
	}
	cfg := sandbox.FromSettings(s, perms, cwd)
	cfg.DenyWrite = append(cfg.DenyWrite, protectSettingsFiles(settingsFiles)...)
	m := sandbox.New(cfg, sandboxOptions(sandbox.Options{Cwd: cwd, Roots: gate.Roots}))
	if err := m.Unavailable(); err != nil {
		if cfg.FailIfUnavailable {
			return nil, fmt.Errorf("sandbox.enabled and sandbox.failIfUnavailable are set, but the sandbox cannot start: %v", err)
		}
		warn(fmt.Sprintf("sandbox.enabled is set, but the sandbox cannot start (%v): bash commands run without it. Set sandbox.failIfUnavailable to refuse to start instead.", err))
		return m, nil
	}
	gate.SetSandbox(m)
	env.Sandbox = m
	setGitSandbox(m.Always())
	m.SetNetworkDecider(gate.ApproveNetwork, gate.SaveNetworkRule)
	return m, nil
}

// protectSettingsFiles is the sandbox's write-deny entries for the
// settings files: each as named and, when it resolves elsewhere through
// symlinks (a ~/.claude/settings.json linked into a dotfiles repository),
// where it really is. The sandbox's own list names only .claude settings
// files, which misses a --settings file and a link target.
func protectSettingsFiles(files []string) []sandbox.Rule {
	var out []sandbox.Rule
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, sandbox.Rule{Path: p})
		}
	}
	for _, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			continue
		}
		add(abs)
		if real, ok := execenv.RealPath(abs); ok {
			add(real)
		}
	}
	return out
}

// sandboxOptions lets a test stand in for the platform (an unsupported
// GOOS, a missing bwrap).
var sandboxOptions = func(o sandbox.Options) sandbox.Options { return o }

// sandboxReport is doctor's sandbox section: the status line and any
// problems.
func sandboxReport(cwd string, s claudesettings.Settings) (line string, problems []string) {
	problems = append(problems, s.SandboxWarnings...)
	if !s.Sandbox.IsEnabled() {
		mech := "sandbox-exec"
		switch runtime.GOOS {
		case "linux":
			mech = "bubblewrap"
		case "darwin":
		default:
			mech = "unsupported on " + runtime.GOOS
		}
		return "off (sandbox.enabled is not set; " + mech + ")", problems
	}
	problems = append(problems, claudesettings.SandboxRuleWarnings(s.Permissions, cwd)...)
	cfg := sandbox.FromSettings(s, s.Permissions, cwd)
	m := sandbox.New(cfg, sandboxOptions(sandbox.Options{Cwd: cwd}))
	if err := m.Unavailable(); err != nil {
		if cfg.FailIfUnavailable {
			problems = append(problems, "sandbox cannot start and failIfUnavailable is set, so kiln refuses to start: "+err.Error())
		} else {
			problems = append(problems, "sandbox is enabled but cannot start, so bash commands run unsandboxed: "+err.Error())
		}
		return "enabled, unavailable: " + err.Error(), problems
	}
	var parts []string
	parts = append(parts, "on ("+m.Mechanism()+")")
	if cfg.AutoAllow {
		parts = append(parts, "auto-allow")
	} else {
		parts = append(parts, "regular permissions")
	}
	if !cfg.AllowUnsandboxed {
		parts = append(parts, "strict (no unsandboxed retry)")
	}
	if n := len(cfg.Excluded); n > 0 {
		parts = append(parts, fmt.Sprintf("%d excluded command pattern(s)", n))
	}
	if n := len(cfg.AllowedDomains); n > 0 {
		parts = append(parts, fmt.Sprintf("%d allowed domain(s)", n))
	} else {
		parts = append(parts, "no allowed domains")
	}
	if cfg.FilesystemDisabled {
		parts = append(parts, "filesystem isolation off")
	}
	if runtime.GOOS == "linux" {
		problems = append(problems, "sandbox: Unix sockets are not filtered on Linux (no seccomp filter); only the bus, docker, podman, gpg and ssh agent sockets are hidden")
	}
	problems = append(problems, cfg.Notes...)
	return strings.Join(parts, ", "), problems
}
