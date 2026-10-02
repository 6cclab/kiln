package permission

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// fakeSandbox is a SandboxPolicy: commands starting with "docker" are
// excluded, everything else runs sandboxed unless it asks not to.
type fakeSandbox struct {
	active, autoAllow, unsandboxed bool
}

func (f fakeSandbox) Active() bool { return f.active }
func (f fakeSandbox) WillSandbox(cmd string, disable bool) bool {
	if !f.active || strings.HasPrefix(cmd, "docker") {
		return false
	}
	return !(disable && f.unsandboxed)
}
func (f fakeSandbox) AutoAllow() bool          { return f.autoAllow }
func (f fakeSandbox) UnsandboxedAllowed() bool { return f.unsandboxed }
func (f fakeSandbox) CriticalRemoval(cmd string) bool {
	return strings.HasPrefix(cmd, "rm -rf .")
}

type promptLog struct {
	reqs   []Request
	answer PromptKind
}

func (p *promptLog) prompter(_ context.Context, req Request) (PromptChoice, error) {
	p.reqs = append(p.reqs, req)
	return PromptChoice{Kind: p.answer}, nil
}

func sandboxGate(t *testing.T, perms settings.Permissions, mode settings.PermissionMode, sb fakeSandbox, prompt *promptLog) *Gate {
	t.Helper()
	g := NewGate(GateOptions{Permissions: perms, Mode: mode, Roots: []string{t.TempDir()}})
	if prompt != nil {
		g.SetPrompter(prompt.prompter)
	}
	g.SetSandbox(sb)
	return g
}

func sandboxBashReq(cmd string, disable bool) Request {
	args := map[string]any{"command": cmd}
	if disable {
		args[DisableSandboxArg] = true
	}
	return Request{ToolName: "bash", PrimaryArg: cmd, Args: args}
}

// TestSandboxAutoAllow follows "Auto-allow mode" in Claude Code's
// sandboxing docs.
func TestSandboxAutoAllow(t *testing.T) {
	ctx := context.Background()
	on := fakeSandbox{active: true, autoAllow: true, unsandboxed: true}
	// A command that writes asks in manual mode without a sandbox...
	cmd := "touch made.txt"
	g := sandboxGate(t, settings.Permissions{}, settings.ModeManual, fakeSandbox{}, nil)
	if r, _ := g.Check(ctx, sandboxBashReq(cmd, false)); r == nil {
		t.Fatal("without a sandbox a writing command must ask (and be refused with no prompter)")
	}
	// ...and runs without a prompt when sandboxed, in manual, acceptEdits
	// and dontAsk mode alike.
	for _, mode := range []settings.PermissionMode{settings.ModeManual, settings.ModeAcceptEdits, settings.ModeDontAsk} {
		g = sandboxGate(t, settings.Permissions{}, mode, on, nil)
		r, out, err := g.CheckWithOutcome(ctx, sandboxBashReq(cmd, false))
		if err != nil || r != nil || out != OutcomeAuto {
			t.Errorf("%s: sandboxed command not auto-allowed: %+v %s %v", mode, r, out, err)
		}
	}
	// autoAllowBashIfSandboxed false: the regular flow.
	g = sandboxGate(t, settings.Permissions{}, settings.ModeManual, fakeSandbox{active: true}, nil)
	if r, _ := g.Check(ctx, sandboxBashReq(cmd, false)); r == nil {
		t.Error("regular permissions mode should still ask")
	}
	// An inactive sandbox (enabled but unavailable) auto-allows nothing.
	g = sandboxGate(t, settings.Permissions{}, settings.ModeManual, fakeSandbox{autoAllow: true}, nil)
	if r, _ := g.Check(ctx, sandboxBashReq(cmd, false)); r == nil {
		t.Error("an inactive sandbox must not auto-allow")
	}
}

func TestSandboxAutoAllowExceptions(t *testing.T) {
	ctx := context.Background()
	on := fakeSandbox{active: true, autoAllow: true, unsandboxed: true}
	cases := []struct {
		name  string
		perms settings.Permissions
		mode  settings.PermissionMode
		cmd   string
		want  string // "allow", "ask", "deny"
	}{
		{"deny rule wins", settings.Permissions{Deny: []string{"Bash(git push *)"}}, settings.ModeManual, "git push origin", "deny"},
		{"deny rule in a compound command", settings.Permissions{Deny: []string{"Bash(rm *)"}}, settings.ModeManual, "echo x && rm -f y", "deny"},
		{"content ask rule asks", settings.Permissions{Ask: []string{"Bash(git push *)"}}, settings.ModeManual, "git push origin", "ask"},
		{"bare Bash ask rule is skipped", settings.Permissions{Ask: []string{"Bash"}}, settings.ModeManual, "touch x", "allow"},
		{"Bash(*) ask rule is skipped", settings.Permissions{Ask: []string{"Bash(*)"}}, settings.ModeManual, "touch x", "allow"},
		{"bare Bash ask rule holds in plan mode", settings.Permissions{Ask: []string{"Bash"}}, settings.ModePlan, "ls", "ask"},
		{"plan mode does not widen", settings.Permissions{}, settings.ModePlan, "touch x", "ask"},
		{"critical rm goes the regular way", settings.Permissions{}, settings.ModeManual, "rm -rf .", "ask"},
		{"excluded command goes the regular way", settings.Permissions{}, settings.ModeManual, "docker compose up", "ask"},
		{"excluded command with an allow rule", settings.Permissions{Allow: []string{"Bash(docker *)"}}, settings.ModeManual, "docker compose up", "allow"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &promptLog{answer: PromptDeny}
			g := sandboxGate(t, c.perms, c.mode, on, p)
			r, _, err := g.CheckWithOutcome(ctx, sandboxBashReq(c.cmd, false))
			if err != nil {
				t.Fatal(err)
			}
			got := "allow"
			switch {
			case len(p.reqs) > 0:
				got = "ask"
			case r != nil:
				got = "deny"
			}
			if got != c.want {
				t.Errorf("got %s (block %+v), want %s", got, r, c.want)
			}
		})
	}
}

// TestSandboxUnsandboxedRetry follows "The unsandboxed retry escape
// hatch": the regular flow, with the prompt marked unsandboxed; dontAsk
// refuses it; an allow rule approves it; an ask rule on
// dangerouslyDisableSandbox:true prompts even in bypassPermissions.
func TestSandboxUnsandboxedRetry(t *testing.T) {
	ctx := context.Background()
	on := fakeSandbox{active: true, autoAllow: true, unsandboxed: true}

	p := &promptLog{answer: PromptAllow}
	g := sandboxGate(t, settings.Permissions{}, settings.ModeManual, on, p)
	if r, _ := g.Check(ctx, sandboxBashReq("touch x", true)); r != nil || len(p.reqs) != 1 || !p.reqs[0].Unsandboxed {
		t.Errorf("manual retry should prompt, marked unsandboxed: %+v %+v", r, p.reqs)
	}

	g = sandboxGate(t, settings.Permissions{}, settings.ModeBypassPermissions, on, nil)
	if r, _ := g.Check(ctx, sandboxBashReq("touch x", true)); r != nil {
		t.Error("bypassPermissions runs the retry without a prompt")
	}

	g = sandboxGate(t, settings.Permissions{}, settings.ModeDontAsk, on, nil)
	if r, _ := g.Check(ctx, sandboxBashReq("touch x", true)); r == nil {
		t.Error("dontAsk must refuse the retry")
	}
	g = sandboxGate(t, settings.Permissions{Allow: []string{"Bash(touch *)"}}, settings.ModeDontAsk, on, nil)
	if r, _ := g.Check(ctx, sandboxBashReq("touch x", true)); r != nil {
		t.Error("an allow rule approves the retry, even in dontAsk")
	}

	askRule := settings.Permissions{Allow: []string{"Bash(touch *)"}, Ask: []string{"Bash(dangerouslyDisableSandbox:true)"}}
	p = &promptLog{answer: PromptDeny}
	g = sandboxGate(t, askRule, settings.ModeBypassPermissions, on, p)
	if r, _ := g.Check(ctx, sandboxBashReq("touch x", true)); r == nil || len(p.reqs) != 1 {
		t.Errorf("the dangerouslyDisableSandbox ask rule must prompt even in bypass: %+v %d", r, len(p.reqs))
	}
	// The same rule does not touch a sandboxed call.
	p = &promptLog{answer: PromptDeny}
	g = sandboxGate(t, askRule, settings.ModeManual, on, p)
	if r, _ := g.Check(ctx, sandboxBashReq("touch x", false)); r != nil || len(p.reqs) != 0 {
		t.Errorf("a sandboxed call should still be auto-allowed: %+v %d", r, len(p.reqs))
	}

	// With allowUnsandboxedCommands false the request is ignored: the call
	// is sandboxed and auto-allowed.
	strict := fakeSandbox{active: true, autoAllow: true}
	g = sandboxGate(t, settings.Permissions{}, settings.ModeManual, strict, nil)
	if r, out, _ := g.CheckWithOutcome(ctx, sandboxBashReq("touch x", true)); r != nil || out != OutcomeAuto {
		t.Errorf("strict sandbox: %+v %s", r, out)
	}
}

// TestApproveNetwork follows "Hosts outside your allowed domains".
func TestApproveNetwork(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		mode    settings.PermissionMode
		prompt  bool
		answer  PromptKind
		allow   bool
		prompts int
	}{
		{settings.ModeBypassPermissions, true, PromptDeny, true, 0},
		{settings.ModeAuto, true, PromptAllow, false, 0},
		{settings.ModeDontAsk, true, PromptAllow, false, 0},
		{settings.ModeManual, true, PromptAllow, true, 1},
		{settings.ModeAcceptEdits, true, PromptDeny, false, 1},
		{settings.ModePlan, true, PromptAllowAlways, true, 1},
		{settings.ModeManual, false, PromptAllow, false, 0}, // print mode
	}
	for _, c := range cases {
		var p *promptLog
		if c.prompt {
			p = &promptLog{answer: c.answer}
		}
		g := sandboxGate(t, settings.Permissions{}, c.mode, fakeSandbox{active: true}, p)
		allow, always, err := g.ApproveNetwork(ctx, "example.com", 443)
		if err != nil || allow != c.allow {
			t.Errorf("%s: allow=%v err=%v, want %v", c.mode, allow, err, c.allow)
		}
		if p != nil && len(p.reqs) != c.prompts {
			t.Errorf("%s: %d prompts, want %d", c.mode, len(p.reqs), c.prompts)
		}
		if p != nil && len(p.reqs) == 1 && (p.reqs[0].ToolName != NetworkToolName || p.reqs[0].PrimaryArg != "example.com:443") {
			t.Errorf("prompt request = %+v", p.reqs[0])
		}
		if always != (c.answer == PromptAllowAlways && c.prompts == 1) {
			t.Errorf("%s: always = %v", c.mode, always)
		}
	}

	var saved []string
	g := sandboxGate(t, settings.Permissions{}, settings.ModeManual, fakeSandbox{active: true}, nil)
	g.SetRuleSaver(func(r string) { saved = append(saved, r) })
	g.SaveNetworkRule("api.example.com")
	if len(saved) != 1 || saved[0] != "WebFetch(domain:api.example.com)" {
		t.Errorf("saved = %v", saved)
	}
	if allow, _ := settings.SandboxRuleDomains(g.Permissions()); len(allow) != 1 {
		t.Errorf("the session's rules should now allow the host: %v", g.Permissions().Allow)
	}
}
