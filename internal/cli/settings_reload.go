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
// --settings file, the same trust gate on a repository-supplied
// .kiln/settings.local.json), so it can apply nothing a restart would not:
// a repository's file cannot widen what only the user's own settings may.
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
	opts := r.opts
	opts.KilnLocalTrusted = r.trusted()
	opts.Quiet = true
	s := claudesettings.LoadSettings(r.cwd, opts)
	if len(s.Unreadable) > 0 {
		msg := fmt.Sprintf("Settings changed, but %s could not be read; permission rules are unchanged until it is fixed.", strings.Join(shortPaths(r.cwd, s.Unreadable), ", "))
		diag.L().Warn("settings reload skipped", "unreadable", strings.Join(s.Unreadable, ", "))
		r.notice(msg)
		return
	}
	r.gate.ReplaceSettingsRules(s.Permissions)
	diag.L().Info("settings reloaded", "changed", strings.Join(changed, ", "),
		"allow", len(s.Permissions.Allow), "deny", len(s.Permissions.Deny), "ask", len(s.Permissions.Ask))
	r.notice(fmt.Sprintf("Settings changed (%s): permission rules reloaded.", strings.Join(shortPaths(r.cwd, changed), ", ")))
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
