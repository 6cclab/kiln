package permission

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
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
	// that skips prompting (bypassPermissions, auto, acceptEdits for
	// edit/write).
	OutcomeAuto Outcome = "auto-approved"
	// OutcomeDeclined means the call reached a human prompt and was
	// answered No. The interface has already reported the refusal (with
	// any feedback), so it need not render the call's result as well.
	OutcomeDeclined Outcome = "declined"
	// OutcomeHookBlocked means a PreToolUse hook refused the call before
	// the gate saw it.
	OutcomeHookBlocked Outcome = "blocked by hook"
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
	// PlanLedgerPath, when non-empty, is the one path plan mode's
	// read-only enforcement exempts: an edit/write call naming exactly
	// this path is allowed even in ModePlan. Empty leaves plan mode fully
	// read-only, as before. This is kiln's HARNESS_EXP_LEDGER experiment
	// switch (internal/cli/experiments.go) — small and easy to delete: it
	// touches only this field, its one check in CheckWithOutcome below,
	// and the call site that sets it.
	PlanLedgerPath string
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

	// planLedgerPath mirrors GateOptions.PlanLedgerPath; see its doc
	// comment.
	planLedgerPath string
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
	if opts.PlanLedgerPath != "" {
		if full, err := filepath.Abs(opts.PlanLedgerPath); err == nil {
			g.planLedgerPath = filepath.Clean(full)
		} else {
			g.planLedgerPath = filepath.Clean(opts.PlanLedgerPath)
		}
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

// cwd is the primary working directory: the first root, where relative
// paths and cwd-anchored permission rules resolve. "" (no roots) lets
// settings fall back to the process's working directory.
func (g *Gate) cwd() string {
	if len(g.roots) > 0 {
		return g.roots[0]
	}
	return ""
}

// WithinRoots reports whether path lies inside any allowed root. path is
// resolved the way the file tools resolve it (execenv.ResolveToolPath), so
// "~/x", "@/x" and file:///x name the file the tool will open, not a
// directory called "~" or "@" under the workspace.
//
// Both the path as written and the path it resolves to through symlinks
// (execenv.RealPath, which follows a dangling final link too) must be
// inside: "proj/sshdir/authorized_keys" with sshdir -> ~/.ssh is outside,
// however it is spelt. A path that cannot be resolved (a symlink loop) is
// outside.
func (g *Gate) WithinRoots(path string) bool {
	base := "."
	if len(g.roots) > 0 {
		base = g.roots[0]
	}
	full := execenv.ResolveToolPath(base, path)
	if !under(full, g.roots) {
		return false
	}
	real, ok := execenv.RealPath(full)
	if !ok {
		return false
	}
	realRoots := make([]string, 0, len(g.roots))
	for _, r := range g.roots {
		rr, _ := execenv.RealPath(r)
		realRoots = append(realRoots, rr)
	}
	return under(real, realRoots)
}

// under reports whether full is one of roots or inside one.
func under(full string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, full)
		if err != nil {
			continue
		}
		// "." means the path IS the root; a leading ".." means it escapes.
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)) {
			return true
		}
	}
	return false
}

// resolvePlanPath makes path absolute and clean, relative to the first
// root when it is not already absolute, matching how WithinRoots resolves
// a relative path. Used only by the planLedgerPath exception, which needs
// to compare a tool call's path argument against g.planLedgerPath (always
// absolute) regardless of whether the model passed it relative or
// absolute.
func (g *Gate) resolvePlanPath(path string) string {
	full := path
	if !filepath.IsAbs(full) {
		base := "."
		if len(g.roots) > 0 {
			base = g.roots[0]
		}
		full = filepath.Join(base, full)
	}
	return filepath.Clean(full)
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

// list returns a rule list and its parallel source list
// (settings.Permissions.AllowFrom etc.).
func (g *Gate) list(list RuleList) (*[]string, *[]settings.RuleSource) {
	switch list {
	case RuleDeny:
		return &g.permissions.Deny, &g.permissions.DenyFrom
	case RuleAsk:
		return &g.permissions.Ask, &g.permissions.AskFrom
	default:
		return &g.permissions.Allow, &g.permissions.AllowFrom
	}
}

// localSource is the source of a rule /permissions saves: the project's
// settings.local.json (internal/cli/commands.go writes it there), which
// anchors "/path" rules at the primary working directory.
func (g *Gate) localSource() settings.RuleSource {
	for _, f := range paths.SettingsFiles(g.cwd()) {
		if f.Scope == paths.ScopeLocal {
			return settings.RuleSource{Scope: paths.ScopeLocal, File: f.Path}
		}
	}
	return settings.RuleSource{}
}

// removable reports whether a rule from src is one RemoveRule may drop:
// one /permissions saved (the local settings file) or a CLI/session rule.
// A rule from user or project settings stays, as it does in its file.
func (g *Gate) removable(src settings.RuleSource) bool {
	return src == settings.RuleSource{} || src == g.localSource()
}

// AddRule adds a rule for the rest of this session, as the local settings
// file's rule (which is where /permissions also saves it). Separate from
// persisting it: the in-memory set is what the next tool call is judged
// against. It is deduplicated on the rule and its source together: the
// same text from user settings is a different rule ("/secrets/**" there
// is under ~/.claude, here under the project).
func (g *Gate) AddRule(list RuleList, rule string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, from := g.list(list)
	src := g.localSource()
	for i, r := range *l {
		if r == rule && sourceAt(*from, i) == src {
			return
		}
	}
	// Give every earlier rule an explicit source entry first, so the new
	// rule's lands at its own index.
	for len(*from) < len(*l) {
		*from = append(*from, settings.RuleSource{})
	}
	*l = append(*l, rule)
	*from = append(*from, src)
}

func sourceAt(from []settings.RuleSource, i int) settings.RuleSource {
	if i < len(from) {
		return from[i]
	}
	return settings.RuleSource{}
}

// RemoveRule removes a rule from the in-memory set: the copies /permissions
// saved or this session added, not the same text from user or project
// settings (writesettings.RemoveRule leaves those files alone too).
func (g *Gate) RemoveRule(list RuleList, rule string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, from := g.list(list)
	out := make([]string, 0, len(*l))
	var outFrom []settings.RuleSource
	for i, r := range *l {
		if r == rule && g.removable(sourceAt(*from, i)) {
			continue
		}
		out = append(out, r)
		// Keep each remaining rule's source aligned with it.
		if i < len(*from) {
			outFrom = append(outFrom, (*from)[i])
		}
	}
	*l, *from = out, outFrom
}

// SessionGrants returns grants made by "yes, don't ask again" this
// session, surfaced because they are invisible otherwise. Each is written
// as a Claude Code permission rule ("Bash(npm test)"), the form a person
// reads in settings.json and the form /permissions saves when one is
// promoted to a deny rule. Sorted, so the list does not reshuffle.
func (g *Gate) SessionGrants() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.sessionAllows))
	for k := range g.sessionAllows {
		out = append(out, grantRule(k))
	}
	sort.Strings(out)
	return out
}

// grantRule turns a session-grant key ("bash::npm test") into rule syntax
// ("Bash(npm test)"). Tool names take Claude Code's spelling: snake_case
// becomes CamelCase ("web_fetch" → "WebFetch"); MCP tool names
// ("mcp__server__tool") are already in that form and stay as they are.
func grantRule(k string) string {
	tool, arg, _ := strings.Cut(k, "::")
	if !strings.HasPrefix(tool, "mcp__") {
		var b strings.Builder
		for _, part := range strings.Split(tool, "_") {
			if part != "" {
				b.WriteString(strings.ToUpper(part[:1]) + part[1:])
			}
		}
		tool = b.String()
	}
	if arg == "" {
		return tool
	}
	return tool + "(" + arg + ")"
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
	return len(permissions.Ask) > 0 && settings.DecideIn(settings.Permissions{Ask: permissions.Ask, AskFrom: permissions.AskFrom}, g.cwd(), "bash", cmd, settings.ModeAuto) == settings.Ask
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

	g.mu.Lock()
	permissions, mode := g.permissions, g.mode
	if len(g.sessionRules) > 0 {
		permissions.Allow = append(append([]string(nil), permissions.Allow...), g.sessionRules...)
	}
	g.mu.Unlock()

	// A file tool is judged on its path argument, resolved the way the
	// tool resolves it (settings/pathrules.go), whatever PrimaryArgOf
	// picked.
	decideArg := req.PrimaryArg
	if settings.IsFileTool(req.ToolName) {
		if p, ok := PathArgOf(req.Args); ok {
			decideArg = p
		}
	}
	denyHit, askHit, allowHit := settings.RuleHits(permissions, g.cwd(), req.ToolName, decideArg)

	// A session "don't ask again" grant stands in for an allow rule, so
	// like one it never beats a deny or ask rule — including one added
	// after the grant (Claude Code: deny, then ask, then allow).
	grantable := !denyHit && !askHit
	if grantable && g.sessionAllowed(k) {
		return nil, OutcomeAuto, nil
	}

	// HARNESS_EXP_LEDGER (internal/cli/experiments.go): the one narrow
	// exception to plan mode's read-only enforcement, checked before
	// Decide so it never has to know about it. Only edit/write, and only
	// for the exact ledger path — every other tool and every other path
	// still hits Decide's ModePlan case below and is refused as usual. A
	// deny rule still wins.
	if mode == settings.ModePlan && g.planLedgerPath != "" && !denyHit &&
		(strings.EqualFold(req.ToolName, "edit") || strings.EqualFold(req.ToolName, "write")) {
		if path, ok := PathArgOf(req.Args); ok && g.resolvePlanPath(path) == g.planLedgerPath {
			return nil, OutcomeAuto, nil
		}
	}

	verdict := settings.DecideFromHits(denyHit, askHit, allowHit, req.ToolName, decideArg, mode)

	// A bash command that provably only reads, and only inside the
	// workspace, runs without asking in the modes that otherwise ask about
	// bash — the way the read tool never asks. Asking before "cat app.py"
	// or "git log" was pure friction. Rules still win: Decide has already
	// returned Deny or an explicit Ask for anything a rule names.
	if verdict == settings.Ask && strings.EqualFold(req.ToolName, "bash") &&
		(mode == settings.ModeManual || mode == settings.ModeAcceptEdits || mode == settings.ModeDontAsk) &&
		settings.IsReadOnlyCommand(req.PrimaryArg) && !g.explicitAsk(permissions, req.PrimaryArg) &&
		g.commandWithinRoots(req.PrimaryArg) {
		return nil, OutcomeAuto, nil
	}

	// A path outside the workspace always warrants a question, even when a
	// rule would otherwise allow the tool.
	path, hasPath := PathArgOf(req.Args)
	escaped := hasPath && !g.WithinRoots(path)
	if escaped && verdict == settings.Allow && mode != settings.ModeBypassPermissions {
		if mode == settings.ModeDontAsk {
			r := g.record(req, fmt.Sprintf("%s is outside the workspace, and don't-ask mode refuses anything that would need approval.", path))
			return &r, OutcomeNone, nil
		}
		if g.prompter == nil {
			r := g.record(req, fmt.Sprintf("%s is outside the workspace and cannot be confirmed.", path))
			return &r, OutcomeNone, nil
		}
		g.promptMu.Lock()
		defer g.promptMu.Unlock()
		if grantable && g.sessionAllowed(k) {
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
	if mode == settings.ModeDontAsk {
		r := g.record(req, "don't-ask mode refuses anything that would need approval. Add an allow rule for it, or switch modes.")
		return &r, OutcomeNone, nil
	}
	if g.prompter == nil {
		// Headless with no way to ask. Refusing beats proceeding: an
		// unattended run must not silently take an action the policy said
		// required confirmation.
		r := g.record(req, "requires confirmation and no prompt is available.")
		return &r, OutcomeNone, nil
	}

	g.promptMu.Lock()
	defer g.promptMu.Unlock()
	if grantable && g.sessionAllowed(k) {
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
