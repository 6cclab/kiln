package cli

import (
	"fmt"
	"strings"

	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/plural"
)

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
