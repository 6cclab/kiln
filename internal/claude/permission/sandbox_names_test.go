package permission

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// Auto-allow never covers a command a deny or ask rule might name: one
// whose name comes from a substitution or a variable, git with global
// options before a denied subcommand, or a package runner running a
// command an ask rule names.
func TestSandboxAutoAllowHiddenNames(t *testing.T) {
	on := fakeSandbox{active: true, autoAllow: true, unsandboxed: true}
	perms := settings.Permissions{Deny: []string{"Bash(curl *)", "Bash(git push *)"}, Ask: []string{"Bash(npm publish *)"}}
	for _, c := range []string{
		"$(echo curl) x", "x=curl; $x evil", "git -C . push origin", "git -c a=b push",
		"git --git-dir=.git push", "npx npm publish x", "npm exec -- npm publish x",
	} {
		g := sandboxGate(t, perms, settings.ModeManual, on, nil)
		r, out, _ := g.CheckWithOutcome(context.Background(), bashReq(c, false))
		if r == nil || out == OutcomeAuto {
			t.Errorf("%q auto-allowed under deny/ask rules", c)
		}
	}
	// Ordinary commands still are.
	g := sandboxGate(t, perms, settings.ModeManual, on, nil)
	if r, out, _ := g.CheckWithOutcome(context.Background(), bashReq("git -C . status", false)); r != nil || out != OutcomeAuto {
		t.Errorf("git -C . status: %+v %s", r, out)
	}
}

// The same matching holds without a sandbox: in bypassPermissions a deny
// rule on "git push" also stops "git -C . push".
func TestDenyRuleSeesGitGlobalOptions(t *testing.T) {
	perms := settings.Permissions{Deny: []string{"Bash(git push *)"}}
	g := sandboxGate(t, perms, settings.ModeBypassPermissions, fakeSandbox{}, nil)
	for _, c := range []string{"git -C . push origin", "git -c a=b push"} {
		if r, _ := g.Check(context.Background(), bashReq(c, false)); r == nil {
			t.Errorf("%q not denied", c)
		}
	}
}
