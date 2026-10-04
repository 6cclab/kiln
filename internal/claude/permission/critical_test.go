package permission

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// An rm or rmdir of a critical path is approved by no allow rule, session
// grant or hook "allow", in any mode, with the sandbox off (the default);
// a deny rule still denies (Claude Code's permission-modes docs, "Critical
// paths"). The check used to run only with a sandbox bound, so a hook
// allow or Bash(rm *) let rm -rf ~ through.
func TestCriticalRemovalNeverApprovedByAllow(t *testing.T) {
	critical := []string{"rm -rf ~", "rm -rf /", "rm -rf /usr", "rmdir ~", "rm -rf ."}
	cases := []struct {
		name  string
		mode  settings.PermissionMode
		perms settings.Permissions
		hook  string
	}{
		{"hook allow, manual", settings.ModeManual, settings.Permissions{}, HookAllow},
		{"hook allow, acceptEdits", settings.ModeAcceptEdits, settings.Permissions{}, HookAllow},
		{"allow rule, manual", settings.ModeManual, settings.Permissions{Allow: []string{"Bash(rm *)", "Bash(rmdir *)"}}, ""},
		{"bare Bash, acceptEdits", settings.ModeAcceptEdits, settings.Permissions{Allow: []string{"Bash"}}, ""},
		{"bypassPermissions", settings.ModeBypassPermissions, settings.Permissions{}, ""},
		{"auto mode, allow rule", settings.ModeAuto, settings.Permissions{Allow: []string{"Bash(rm *)", "Bash(rmdir *)"}}, ""},
		{"auto mode, hook allow", settings.ModeAuto, settings.Permissions{}, HookAllow},
	}
	for _, c := range cases {
		for _, cmd := range critical {
			cl := allowAll()
			g, p, _ := hookGate(t, c.mode, c.perms, cl)
			p.kind = PromptAllowAlways
			blocked, out, err := g.CheckWithOutcome(context.Background(), hookReq(bashReq(cmd), c.hook, ""))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.reqs) != 1 || out != OutcomeApproved || blocked != nil {
				t.Errorf("%s: %q prompts=%d out=%q, want the user asked", c.name, cmd, len(p.reqs), out)
				continue
			}
			if r := p.reqs[0]; r.Grantable || !r.ModeSwitchMoot || r.AutoModeNote != criticalNote {
				t.Errorf("%s: %q grantable=%v moot=%v note=%q", c.name, cmd, r.Grantable, r.ModeSwitchMoot, r.AutoModeNote)
			}
			if len(cl.calls) != 0 {
				t.Errorf("%s: %q went to the classifier", c.name, cmd)
			}
		}
	}

	t.Run("dontAsk refuses it past an allow rule", func(t *testing.T) {
		g, p, _ := hookGate(t, settings.ModeDontAsk, settings.Permissions{Allow: []string{"Bash(rm *)"}}, nil)
		if blocked, _, _ := g.CheckWithOutcome(context.Background(), bashReq("rm -rf ~")); blocked == nil || len(p.reqs) != 0 {
			t.Errorf("blocked=%+v prompts=%d, want refused", blocked, len(p.reqs))
		}
	})
	t.Run("a deny rule still denies", func(t *testing.T) {
		g, p, _ := hookGate(t, settings.ModeManual, settings.Permissions{Deny: []string{"Bash(rm *)"}}, nil)
		if blocked, _, _ := g.CheckWithOutcome(context.Background(), bashReq("rm -rf ~")); blocked == nil || len(p.reqs) != 0 {
			t.Errorf("blocked=%+v prompts=%d, want denied", blocked, len(p.reqs))
		}
	})
	t.Run("an ordinary removal is unaffected", func(t *testing.T) {
		g, p, _ := hookGate(t, settings.ModeManual, settings.Permissions{Allow: []string{"Bash(rm *)"}}, nil)
		if blocked, out, _ := g.CheckWithOutcome(context.Background(), bashReq("rm -rf build")); blocked != nil || out != OutcomeAuto || len(p.reqs) != 0 {
			t.Errorf("blocked=%+v out=%q prompts=%d, want the allow rule to apply", blocked, out, len(p.reqs))
		}
	})
}
