package permission

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/andrepato/harness/internal/claude/settings"
)

// PromptChoice is the user's answer to a permission prompt.
type PromptChoice struct {
	Kind PromptKind
	// Feedback is set only for Deny: what to tell the model instead.
	Feedback string
}

type PromptKind string

const (
	PromptAllow       PromptKind = "allow"
	PromptAllowAlways PromptKind = "allow-always"
	PromptDeny        PromptKind = "deny"
)

// Request describes one permission check.
type Request struct {
	ToolName string
	// OutsideWorkspace is set when the target path lies outside the
	// workspace roots.
	OutsideWorkspace bool
	// PrimaryArg is the identifying argument, e.g. the command or file path.
	PrimaryArg string
	// Args carries the full arguments, for rendering a diff or command.
	Args map[string]any
}

// Prompter asks the user. Implemented by the TUI; absent in headless runs.
type Prompter func(ctx context.Context, req Request) (PromptChoice, error)

// BlockResult is a refusal, with the reason written for the model.
type BlockResult struct {
	Reason string
}

// Outcome describes how a Check decision was reached, for a caller that
// wants to render it (the kiln TUI's tool-block meta: "approved" /
// "auto-approved"). It carries no information Check did not already use to
// decide — it is a report of which branch fired, not a new policy.
type Outcome string

const (
	// OutcomeNone means no gate decision applies to this call worth
	// surfacing: a read-only tool, or a call a rule or mode blocked.
	OutcomeNone Outcome = ""
	// OutcomeApproved means the call reached a human prompt and was
	// answered Yes or Yes-always, this time.
	OutcomeApproved Outcome = "approved"
	// OutcomeAuto means the call proceeded without asking: an existing
	// session "always allow" grant, a permission-rules allow, or a mode
	// that skips prompting (bypassPermissions, dontAsk, auto, acceptEdits
	// for edit/write).
	OutcomeAuto Outcome = "auto-approved"
	// OutcomeDeclined means the call reached a human prompt and was
	// answered No. The interface has already reported the refusal (with
	// any feedback), so it need not render the call's result as well.
	OutcomeDeclined Outcome = "declined"
)

// GateOptions configures a Gate.
type GateOptions struct {
	Permissions settings.Permissions
	// Roots are directories tools may touch without asking. Defaults to
	// []string{cwd} — callers should pass the current directory explicitly
	// since Go has no implicit process.cwd() equivalent here.
	Roots  []string
	Mode   settings.PermissionMode
	Prompt Prompter
}

// Gate is the permission gate for tool calls: pi's before_tool hook.
//
// The rule that matters most: a denial is not an error. Blocking returns a
// reason to the model and the turn continues, so the user can redirect
// rather than having the whole run collapse.
type Gate struct {
	permissions settings.Permissions
	prompter    Prompter
	mode        settings.PermissionMode

	// mu guards sessionAllows and blockLog, which concurrent tool calls
	// (from a Concurrent-tool run in the harness turn loop) can now touch
	// from more than one goroutine at once.
	mu sync.Mutex

	// sessionAllows are grants added by "yes, don't ask again", scoped to
	// this session only. Deliberately not persisted: a permission granted
	// in a hurry to unblock one task should not silently become permanent
	// policy.
	sessionAllows map[string]bool
	// sessionRules are bash allow rules granted by "yes, don't ask again"
	// (settings.BashDontAskRule: the prefix the prompt names), judged per
	// command segment with the configured allow rules.
	sessionRules []string

	// blockLog is everything refused this session, for diagnostics.
	blockLog []string

	// roots are directories tools may operate in freely. This is a real
	// boundary, not bookkeeping: without this check a model could read
	// ~/.ssh/id_rsa under an allow:[Read] rule the user only meant to apply
	// to their project.
	roots []string

	// promptMu is held across an entire prompter round trip (both prompt
	// branches of Check), so two concurrent Check calls asking about the
	// same or different requests never show the user two dialogs at once.
	// It is a separate lock from mu: mu is never held while the prompter
	// runs (the prompter can take arbitrarily long, and may itself call
	// back into the gate). The second waiter re-checks sessionAllows after
	// acquiring promptMu, so a grant the first waiter just made is honored
	// without asking again.
	promptMu sync.Mutex
}

// NewGate builds a Gate. Roots are resolved to absolute paths and
// deduplicated.
func NewGate(opts GateOptions) *Gate {
	g := &Gate{
		permissions:   opts.Permissions,
		mode:          opts.Mode,
		prompter:      opts.Prompt,
		sessionAllows: map[string]bool{},
	}
	if g.mode == "" {
		if opts.Permissions.DefaultMode != "" {
			g.mode = opts.Permissions.DefaultMode
		} else {
			g.mode = settings.ModeManual
		}
	}
	for _, r := range opts.Roots {
		g.AddRoot(r)
	}
	return g
}

// AddRoot widens the workspace. Returns the resolved absolute path
// actually added.
func (g *Gate) AddRoot(dir string) string {
	full, err := filepath.Abs(dir)
	if err != nil {
		full = dir
	}
	for _, r := range g.roots {
		if r == full {
			return full
		}
	}
	g.roots = append(g.roots, full)
	return full
}

// Roots returns the current workspace roots.
func (g *Gate) Roots() []string {
	out := make([]string, len(g.roots))
	copy(out, g.roots)
	return out
}

// WithinRoots reports whether path lies inside any allowed root.
func (g *Gate) WithinRoots(path string) bool {
	full := path
	if !filepath.IsAbs(full) {
		base := "."
		if len(g.roots) > 0 {
			base = g.roots[0]
		}
		full = filepath.Join(base, full)
	}
	full = filepath.Clean(full)
	for _, root := range g.roots {
		rel, err := filepath.Rel(root, full)
		if err != nil {
			continue
		}
		// Empty means the path IS the root; a leading ".." means it escapes.
		if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
			return true
		}
	}
	return false
}

// SetMode sets the active permission mode.
func (g *Gate) SetMode(mode settings.PermissionMode) {
	g.mu.Lock()
	g.mode = mode
	g.mu.Unlock()
}

// Mode returns the active permission mode.
func (g *Gate) Mode() settings.PermissionMode {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mode
}

// SetPrompter binds the UI that asks the user. The gate is often
// constructed before the TUI, so the prompter arrives later rather than at
// construction.
func (g *Gate) SetPrompter(p Prompter) { g.prompter = p }

// Permissions returns the merged rules, for /permissions.
func (g *Gate) Permissions() settings.Permissions {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.permissions
}

// RuleList selects which list AddRule/RemoveRule operate on.
type RuleList string

const (
	RuleAllow RuleList = "allow"
	RuleDeny  RuleList = "deny"
	RuleAsk   RuleList = "ask"
)

func (g *Gate) list(list RuleList) *[]string {
	switch list {
	case RuleAllow:
		return &g.permissions.Allow
	case RuleDeny:
		return &g.permissions.Deny
	case RuleAsk:
		return &g.permissions.Ask
	default:
		return &g.permissions.Allow
	}
}

// AddRule adds a rule for the rest of this session. Separate from
// persisting it: the in-memory set is what the next tool call is judged
// against.
func (g *Gate) AddRule(list RuleList, rule string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	l := g.list(list)
	for _, r := range *l {
		if r == rule {
			return
		}
	}
	*l = append(*l, rule)
}

// RemoveRule removes a rule from the in-memory set.
func (g *Gate) RemoveRule(list RuleList, rule string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	l := g.list(list)
	out := make([]string, 0, len(*l))
	for _, r := range *l {
		if r != rule {
			out = append(out, r)
		}
	}
	*l = out
}

// SessionGrants returns grants made by "yes, don't ask again" this
// session, surfaced because they are invisible otherwise.
func (g *Gate) SessionGrants() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.sessionAllows))
	for k := range g.sessionAllows {
		out = append(out, k)
	}
	return out
}

// Blocked returns the refusal log, for /permissions-style reporting.
func (g *Gate) Blocked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.blockLog))
	copy(out, g.blockLog)
	return out
}

// record appends to blockLog under mu. Callers must not hold mu already.
func (g *Gate) record(req Request, reason string) BlockResult {
	g.mu.Lock()
	g.blockLog = append(g.blockLog, fmt.Sprintf("%s(%s): %s", req.ToolName, req.PrimaryArg, reason))
	g.mu.Unlock()
	return BlockResult{Reason: reason}
}

// sessionAllowed reports whether key(toolName, primaryArg) was already
// granted this session.
func (g *Gate) sessionAllowed(k string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessionAllows[k]
}

// grantSession records a "don't ask again" grant.
func (g *Gate) grantSession(k string) {
	g.mu.Lock()
	g.sessionAllows[k] = true
	g.mu.Unlock()
}

// key scopes a session grant by tool plus argument, so "always" is not a
// blank cheque.
func key(toolName, primaryArg string) string {
	return toolName + "::" + primaryArg
}

// PathArgOf returns the path a call targets, if any. Used for the
// workspace-boundary check.
func PathArgOf(args map[string]any) (string, bool) {
	for _, k := range []string{"path", "file_path", "filePath"} {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok {
				return s, true
			}
		}
	}
	return "", false
}

// PrimaryArgOf returns the identifying argument for a call, mirroring what
// the transcript shows. Permission rules match on this.
func PrimaryArgOf(args map[string]any) (string, bool) {
	for _, k := range []string{"command", "path", "file_path", "filePath", "pattern", "query", "url"} {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok {
				return s, true
			}
		}
	}
	return "", false
}

// explicitAsk reports whether an ask rule names this bash command, which
// the read-only allowance must not override.
func (g *Gate) explicitAsk(permissions settings.Permissions, cmd string) bool {
	return len(permissions.Ask) > 0 && settings.Decide(settings.Permissions{Ask: permissions.Ask}, "bash", cmd, settings.ModeAuto) == settings.Ask
}

// commandWithinRoots reports whether every path a command names stays in
// the workspace: absolute and ~ paths must lie inside a root (/dev/null
// aside), and a relative one must not climb out with "..". A cd target is
// a path like any other, so "cd /elsewhere && cat x" is outside.
func (g *Gate) commandWithinRoots(cmd string) bool {
	words, ok := settings.CommandWords(cmd)
	if !ok {
		return false
	}
	home, _ := os.UserHomeDir()
	for _, w := range words {
		if _, v, ok := strings.Cut(w, "="); ok && strings.HasPrefix(w, "-") {
			w = v // --output=/x
		}
		switch {
		case w == "/dev/null":
		case strings.HasPrefix(w, "~"):
			if home == "" || !g.WithinRoots(filepath.Join(home, strings.TrimPrefix(w, "~"))) {
				return false
			}
		case filepath.IsAbs(w):
			if !g.WithinRoots(w) {
				return false
			}
		case w == ".." || strings.HasPrefix(w, "../") || strings.Contains(w, "/../") || strings.HasSuffix(w, "/.."):
			return false
		}
	}
	return true
}

// Check decides, prompting if necessary. A nil result means proceed; a
// non-nil BlockResult carries the reason, written for the model.
//
// Two Concurrent tool calls from the same assistant message can call Check
// at the same time. mu (via sessionAllowed/grantSession) guards the
// grant/block bookkeeping; promptMu is held across an entire prompter round
// trip so two concurrent asks never show the user two dialogs at once, and
// each prompt branch re-checks sessionAllowed after acquiring promptMu so a
// grant the first waiter just won is honored for the second without asking
// again. mu is never held while the prompter runs.
func (g *Gate) Check(ctx context.Context, req Request) (*BlockResult, error) {
	r, _, err := g.CheckWithOutcome(ctx, req)
	return r, err
}

// CheckWithOutcome is Check plus the Outcome that produced the decision,
// for a caller (the kiln TUI, via the gate-wrapper hook in
// internal/cli/chat.go) that wants to report "approved"/"auto-approved" on
// the tool block that follows.
func (g *Gate) CheckWithOutcome(ctx context.Context, req Request) (*BlockResult, Outcome, error) {
	k := key(req.ToolName, req.PrimaryArg)
	if g.sessionAllowed(k) {
		return nil, OutcomeAuto, nil
	}

	g.mu.Lock()
	permissions, mode := g.permissions, g.mode
	if len(g.sessionRules) > 0 {
		permissions.Allow = append(append([]string(nil), permissions.Allow...), g.sessionRules...)
	}
	g.mu.Unlock()

	verdict := settings.Decide(permissions, req.ToolName, req.PrimaryArg, mode)

	// A bash command that provably only reads, and only inside the
	// workspace, runs without asking in the modes that otherwise ask about
	// bash — the way the read tool never asks. Asking before "cat app.py"
	// or "git log" was pure friction. Rules still win: Decide has already
	// returned Deny or an explicit Ask for anything a rule names.
	if verdict == settings.Ask && strings.EqualFold(req.ToolName, "bash") &&
		(mode == settings.ModeManual || mode == settings.ModeAcceptEdits) &&
		settings.IsReadOnlyCommand(req.PrimaryArg) && !g.explicitAsk(permissions, req.PrimaryArg) &&
		g.commandWithinRoots(req.PrimaryArg) {
		return nil, OutcomeAuto, nil
	}

	// A path outside the workspace always warrants a question, even when a
	// rule would otherwise allow the tool.
	path, hasPath := PathArgOf(req.Args)
	escaped := hasPath && !g.WithinRoots(path)
	if escaped && verdict == settings.Allow && mode != settings.ModeBypassPermissions {
		if g.prompter == nil {
			r := g.record(req, fmt.Sprintf("%s is outside the workspace and cannot be confirmed.", path))
			return &r, OutcomeNone, nil
		}
		g.promptMu.Lock()
		defer g.promptMu.Unlock()
		if g.sessionAllowed(k) {
			return nil, OutcomeAuto, nil
		}
		promptReq := req
		promptReq.OutsideWorkspace = true
		choice, err := g.prompter(ctx, promptReq)
		if err != nil {
			return nil, OutcomeNone, err
		}
		if choice.Kind == PromptDeny {
			r := g.record(req, "the user declined access to a path outside the workspace.")
			return &r, OutcomeDeclined, nil
		}
		if choice.Kind == PromptAllowAlways {
			g.grantSession(k)
		}
		return nil, OutcomeApproved, nil
	}

	if verdict == settings.Allow {
		// Read-only tools (settings.ReadOnly) are never gated in any mode —
		// nothing to report. Anything else that reached Allow without
		// asking got there via a rule, bypass mode, or a mode that skips
		// prompting: auto-approved.
		if settings.ReadOnly[strings.ToLower(req.ToolName)] {
			return nil, OutcomeNone, nil
		}
		return nil, OutcomeAuto, nil
	}
	if verdict == settings.Deny {
		reason := "blocked by permission rules."
		if mode == settings.ModePlan {
			reason = fmt.Sprintf("plan mode is read-only, so %s is not available. Describe the change instead of making it.", req.ToolName)
		}
		r := g.record(req, reason)
		return &r, OutcomeNone, nil
	}

	// verdict == ask
	if g.prompter == nil {
		// Headless with no way to ask. Refusing beats proceeding: an
		// unattended run must not silently take an action the policy said
		// required confirmation.
		r := g.record(req, "requires confirmation and no prompt is available.")
		return &r, OutcomeNone, nil
	}

	g.promptMu.Lock()
	defer g.promptMu.Unlock()
	if g.sessionAllowed(k) {
		return nil, OutcomeAuto, nil
	}
	choice, err := g.prompter(ctx, req)
	if err != nil {
		return nil, OutcomeNone, err
	}
	if choice.Kind == PromptAllow {
		return nil, OutcomeApproved, nil
	}
	if choice.Kind == PromptAllowAlways {
		g.grantSession(k)
		if strings.EqualFold(req.ToolName, "bash") {
			g.mu.Lock()
			g.sessionRules = append(g.sessionRules, "Bash("+settings.BashDontAskRule(req.PrimaryArg)+")")
			g.mu.Unlock()
		}
		return nil, OutcomeApproved, nil
	}

	reason := "the user declined. Ask what they would prefer before trying again."
	if choice.Feedback != "" {
		reason = fmt.Sprintf("the user declined and said: %s", choice.Feedback)
	}
	r := g.record(req, reason)
	return &r, OutcomeDeclined, nil
}
