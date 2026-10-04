package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/diag"
)

// settingsReloader re-reads the permission rules when a settings file
// changes mid-session, as Claude Code does (its settings change detector:
// rules from settings files are replaced by what the files now say;
// command-line and session rules stay). Only the rule lists reload. The
// mode, model, env, hooks, sandbox and auto mode's classifier settings are
// read once at startup, and a change to them still needs a restart.
//
// The reload reads exactly what startup read (the same scopes, the same
// --settings file, the same trust gate on the allow rules a repository
// can supply), so it can apply nothing a restart would not: before the
// folder is trusted, an allow rule added to the project's file waits for
// trust like the ones it already had.
type settingsReloader struct {
	cwd  string
	opts claudesettings.LoadOptions
	gate *permission.Gate
	// trusted reports whether the folder's held rules apply now (trusted
	// at startup, or the trust dialog was accepted since).
	trusted func() bool
	notice  func(string)
}

// reload applies the files' current rules. A file that exists but does
// not parse (a save caught halfway, or a typo) keeps every rule as it was:
// dropping that file's deny rules until it is fixed would fail open.
func (r settingsReloader) reload(changed []string) {
	s, ok := r.load("Settings changed")
	if !ok {
		return
	}
	diag.L().Info("settings reloaded", "changed", strings.Join(changed, ", "),
		"allow", len(s.Permissions.Allow), "deny", len(s.Permissions.Deny), "ask", len(s.Permissions.Ask))
	r.notice(fmt.Sprintf("Settings changed (%s): permission rules reloaded.", strings.Join(shortPaths(r.cwd, changed), ", ")))
}

// applyTrust applies the rules held until the folder was trusted (the
// trust dialog was just accepted, and r.trusted now says so) as the files
// say now: a file edited since startup counts as it is, not as it was.
func (r settingsReloader) applyTrust() {
	if s, ok := r.load("Folder trusted"); ok {
		diag.L().Info("settings applied on trust", "allow", len(s.Permissions.Allow))
	}
}

// load reads the files and swaps their rules into the gate. A file that
// could not be read leaves the gate as it was, says so (after what, the
// note's opening), and returns false.
func (r settingsReloader) load(what string) (claudesettings.Settings, bool) {
	opts := r.opts
	opts.Trusted = r.trusted()
	opts.Quiet = true
	s := claudesettings.LoadSettings(r.cwd, opts)
	if len(s.Unreadable) > 0 {
		msg := fmt.Sprintf("%s, but %s could not be read; permission rules are unchanged until it is fixed.", what, strings.Join(shortPaths(r.cwd, s.Unreadable), ", "))
		diag.L().Warn("settings reload skipped", "unreadable", strings.Join(s.Unreadable, ", "))
		r.notice(msg)
		return s, false
	}
	r.gate.ReplaceSettingsRules(s.Permissions)
	return s, true
}

// watch reloads on every change until ctx is done.
func (r settingsReloader) watch(ctx context.Context, interval time.Duration) {
	claudesettings.WatchFiles(ctx, claudesettings.SettingsFiles(r.cwd, r.opts), interval, r.reload)
}

// shortPaths names files relative to cwd when under it, and with ~ for
// the home directory.
func shortPaths(cwd string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, shortPath(cwd, f))
	}
	return out
}

func shortPath(cwd, f string) string {
	if rel, err := filepath.Rel(cwd, f); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return rel
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, f); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return filepath.Join("~", rel)
		}
	}
	return f
}
