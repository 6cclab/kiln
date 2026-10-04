package permission

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/claude/settings"
)

// SandboxPolicy is what the gate needs from the session's OS sandbox
// (internal/sandbox.Manager).
type SandboxPolicy interface {
	// Active reports that commands are being sandboxed.
	Active() bool
	// WillSandbox reports whether a bash call runs inside the sandbox.
	WillSandbox(command string, disable bool) bool
	// AutoAllow is sandbox.autoAllowBashIfSandboxed.
	AutoAllow() bool
	// UnsandboxedAllowed is sandbox.allowUnsandboxedCommands.
	UnsandboxedAllowed() bool
	// CriticalRemoval reports an rm/rmdir of a critical path.
	CriticalRemoval(command string) bool
}

// SetSandbox binds the session's sandbox.
func (g *Gate) SetSandbox(p SandboxPolicy) {
	g.mu.Lock()
	g.sandbox = p
	g.mu.Unlock()
}

// DisableSandboxArg is the bash tools' parameter asking to run a command
// outside the sandbox (Claude Code's dangerouslyDisableSandbox).
const DisableSandboxArg = "dangerouslyDisableSandbox"

// disableSandboxAskRule is the ask rule that makes every unsandboxed retry
// prompt, even in bypassPermissions mode and over a matching allow rule.
const disableSandboxAskRule = "dangerouslydisablesandbox:true"

func (g *Gate) sandboxPolicy() SandboxPolicy {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sandbox
}

func isBashCall(req Request) bool {
	return settings.IsBashTool(req.ToolName) || strings.EqualFold(req.ToolName, "bash_background")
}

// disableRequested reads dangerouslyDisableSandbox by its exact name. The
// tool decodes it case-insensitively; the turn loop's input check
// (tool.CheckArgs) refuses any other spelling of a declared parameter
// before the gate or the tool sees the call, so the two agree.
func disableRequested(req Request) bool {
	b, _ := req.Args[DisableSandboxArg].(bool)
	return b
}

// annotateSandbox marks a bash call that will run outside an active
// sandbox, so its prompt can say so (Claude Code titles it "Bash command
// (unsandboxed)").
func (g *Gate) annotateSandbox(req Request) Request {
	p := g.sandboxPolicy()
	if p == nil || !p.Active() || !isBashCall(req) {
		return req
	}
	req.Unsandboxed = !p.WillSandbox(req.PrimaryArg, disableRequested(req))
	return req
}

// checkSandboxed applies the sandbox's part of the permission flow, as
// Claude Code documents it (code.claude.com/docs/en/sandboxing, "Sandbox
// modes" and "The unsandboxed retry escape hatch"). done reports that it
// decided; otherwise the regular flow continues.
//
// A command that runs sandboxed, with autoAllowBashIfSandboxed on, runs
// without a prompt, except that:
//   - a deny rule still denies it;
//   - an ask rule on its content (Bash(git push *), or a Read/Edit rule on
//     a file it names) still asks; a bare Bash or Bash(*) ask rule is
//     skipped for it, outside plan mode;
//   - an rm/rmdir of a critical path goes through the regular flow;
//   - a write to a protected path kiln can name (prot, from
//     Gate.protectedWrite, which runs first) goes through the regular
//     flow, where it asks, is refused where nobody can be asked, or in
//     auto mode is classified: the sandbox's own protected list is
//     narrower, since it must leave git and builds working;
//   - in plan mode auto-allow does not widen approvals.
//
// In auto mode it is approved here too, before the classifier: Claude
// Code's auto mode approves sandboxed shell commands ahead of its
// classifier step (code.claude.com/docs/en/permission-modes, "How the
// classifier evaluates actions"); its exceptions, server-side review and
// per-command allowed domains, do not exist in kiln. Commands outside the
// sandbox take the regular flow, where auto mode's classifier reviews them.
//
// A command that asked to run unsandboxed (dangerouslyDisableSandbox,
// honoured only when allowUnsandboxedCommands is on) goes through the
// regular flow, with two additions: an ask rule
// Bash(dangerouslyDisableSandbox:true) makes it prompt in every mode and
// over a matching allow rule, and dontAsk mode refuses it unless an allow
// rule covers the command.
func (g *Gate) checkSandboxed(ctx context.Context, req Request, permissions settings.Permissions, mode settings.PermissionMode, hits settings.Hits, prot protection) (*BlockResult, Outcome, bool, error) {
	p := g.sandboxPolicy()
	if p == nil || !p.Active() || !isBashCall(req) {
		return nil, OutcomeNone, false, nil
	}
	disable := disableRequested(req)
	if p.WillSandbox(req.PrimaryArg, disable) {
		if !p.AutoAllow() || mode == settings.ModePlan {
			return nil, OutcomeNone, false, nil
		}
		if hits.Deny {
			r := g.record(req, "blocked by permission rules.")
			return &r, OutcomeNone, true, nil
		}
		// Unsure: something runs that kiln cannot name (a command word
		// from a substitution or a variable) while deny or ask rules
		// exist; it might be what they name, so it is not auto-allowed.
		if hits.Unsure || contentAskHit(permissions, g.cwd(), req) || p.CriticalRemoval(req.PrimaryArg) || prot != unprotected {
			return nil, OutcomeNone, false, nil
		}
		// Auto mode also classifies, whatever allows it, a command whose
		// writes kiln cannot name or that changes git's configuration
		// (bashTouchesProtected, the same rule the classifier step uses for
		// an allow rule); the sandbox does not exempt it from that review.
		if mode == settings.ModeAuto && g.bashTouchesProtected(req.PrimaryArg) {
			return nil, OutcomeNone, false, nil
		}
		return nil, OutcomeAuto, true, nil
	}
	if !disable || !p.UnsandboxedAllowed() || hits.Deny {
		return nil, OutcomeNone, false, nil
	}
	if hasDisableSandboxAskRule(permissions) {
		r, out, err := g.askUnsandboxed(ctx, req, permissions, mode)
		return r, out, true, err
	}
	if mode == settings.ModeDontAsk && !hits.Allow {
		r := g.record(req, "don't-ask mode refuses running a command outside the sandbox. Run it sandboxed, or add an allow rule for it.")
		return &r, OutcomeNone, true, nil
	}
	return nil, OutcomeNone, false, nil
}

// contentAskHit reports an ask rule that matches the command's content:
// every ask rule except a bare Bash (or Bash(*)) one.
func contentAskHit(permissions settings.Permissions, cwd string, req Request) bool {
	filtered := permissions
	filtered.Ask, filtered.AskFrom = nil, nil
	for i, r := range permissions.Ask {
		if bareBashRule(r) {
			continue
		}
		filtered.Ask = append(filtered.Ask, r)
		if i < len(permissions.AskFrom) {
			filtered.AskFrom = append(filtered.AskFrom, permissions.AskFrom[i])
		} else {
			filtered.AskFrom = append(filtered.AskFrom, settings.RuleSource{})
		}
	}
	return settings.RuleHits(filtered, cwd, "bash", req.PrimaryArg).Ask
}

func bareBashRule(r string) bool {
	s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(r), " ", ""))
	return s == "bash" || s == "bash(*)" || s == "bash()"
}

func hasDisableSandboxAskRule(permissions settings.Permissions) bool {
	for _, r := range permissions.Ask {
		s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(r), " ", ""))
		if s == "bash("+disableSandboxAskRule+")" {
			return true
		}
	}
	return false
}

// askUnsandboxed prompts for an unsandboxed retry whatever the mode, as a
// Bash(dangerouslyDisableSandbox:true) ask rule requires.
func (g *Gate) askUnsandboxed(ctx context.Context, req Request, permissions settings.Permissions, mode settings.PermissionMode) (*BlockResult, Outcome, error) {
	if mode == settings.ModeDontAsk {
		r := g.record(req, "don't-ask mode refuses running a command outside the sandbox.")
		return &r, OutcomeNone, nil
	}
	if g.prompter == nil {
		r := g.record(req, "running a command outside the sandbox requires confirmation and no prompt is available.")
		return &r, OutcomeNone, nil
	}
	g.promptMu.Lock()
	defer g.promptMu.Unlock()
	promptReq := req
	promptReq.Grantable, promptReq.DontAskRules = false, nil
	choice, err := g.prompter(ctx, promptReq)
	if err != nil {
		return nil, OutcomeNone, err
	}
	if choice.Kind == PromptDeny {
		reason := "the user declined running this command outside the sandbox."
		if choice.Feedback != "" {
			reason = fmt.Sprintf("the user declined and said: %s", choice.Feedback)
		}
		r := g.record(req, reason)
		return &r, OutcomeDeclined, nil
	}
	return nil, OutcomeApproved, nil
}

// NetworkToolName is the tool name a sandbox network approval is asked
// under.
const NetworkToolName = "sandbox_network"

// ApproveNetwork decides a host a sandboxed command connects to that no
// domain list settles, by permission mode, as Claude Code's sandboxing
// docs tabulate ("Hosts outside your allowed domains"): bypassPermissions
// allows it; manual, acceptEdits and plan ask the user; auto and dontAsk
// refuse it; with nobody to ask (print mode) it is refused. always
// reports a "don't ask again" answer.
func (g *Gate) ApproveNetwork(ctx context.Context, host string, port int) (allow, always bool, err error) {
	switch g.Mode() {
	case settings.ModeBypassPermissions:
		return true, false, nil
	case settings.ModeAuto, settings.ModeDontAsk:
		return false, false, nil
	}
	if g.prompter == nil {
		return false, false, nil
	}
	g.promptMu.Lock()
	defer g.promptMu.Unlock()
	target := host
	if port != 0 {
		target = host + ":" + strconv.Itoa(port)
	}
	req := Request{
		ToolName:   NetworkToolName,
		PrimaryArg: target,
		Args:       map[string]any{"host": host, "port": port},
		Grantable:  true,
	}
	choice, err := g.prompter(ctx, req)
	if err != nil {
		return false, false, err
	}
	switch choice.Kind {
	case PromptAllow:
		return true, false, nil
	case PromptAllowAlways:
		return true, true, nil
	}
	g.record(req, "the user declined network access to "+target)
	return false, false, nil
}

// SaveNetworkRule records "don't ask again" for a host as Claude Code
// does: a WebFetch(domain:host) allow rule, kept for this session and
// persisted through SaveRule (kiln's .kiln/settings.local.json).
func (g *Gate) SaveNetworkRule(host string) {
	rule := "WebFetch(domain:" + host + ")"
	g.AddRule(RuleAllow, rule)
	g.mu.Lock()
	save := g.saveRule
	g.mu.Unlock()
	if save != nil {
		save(rule)
	}
}
