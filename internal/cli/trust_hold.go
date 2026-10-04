package cli

import (
	"fmt"
	"strings"
	"sync/atomic"

	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/plural"
)

// trustedHooks holds the session's hooks until the folder is trusted.
// Claude Code runs no hook in an interactive session before its trust
// dialog is accepted (user hooks included: the dialog comes first), and
// every hook in a -p run, which never shows the dialog. Hooks run commands
// a repository can ship, and a PreToolUse hook's allow is honoured, so an
// untrusted project hook would otherwise both run code and widen what is
// allowed.
type trustedHooks struct {
	cfg claudehooks.Config
	on  atomic.Bool
}

func newTrustedHooks(cfg claudehooks.Config, active bool) *trustedHooks {
	h := &trustedHooks{cfg: cfg}
	h.on.Store(active)
	return h
}

// get is the hooks to run now: none until enable.
func (h *trustedHooks) get() claudehooks.Config {
	if !h.on.Load() {
		return nil
	}
	return h.cfg
}

// enable starts running the hooks (the trust dialog was accepted).
func (h *trustedHooks) enable() { h.on.Store(true) }

// heldRulesWarning says which allow rules wait for trust, "" when none
// do. Under -p it says how to trust the folder, since no dialog will.
func heldRulesWarning(cwd string, s claudesettings.Settings, headless bool) string {
	if len(s.HeldAllow) == 0 {
		return ""
	}
	var files []string
	seen := map[string]bool{}
	for _, src := range s.HeldFrom {
		if f := shortPath(cwd, src.File); src.File != "" && !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	msg := fmt.Sprintf("Ignoring %s from %s: this folder has not been trusted",
		plural.Count(len(s.HeldAllow), "permissions.allow rule"), strings.Join(files, ", "))
	if headless {
		return msg + ". Run kiln here interactively once and trust it."
	}
	return msg + " yet; they apply once you trust it."
}
