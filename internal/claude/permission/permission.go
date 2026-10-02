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
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
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
	// Grantable is set by the gate on a request it prompts for when a
	// "Yes, and don't ask again" answer would be honoured: no deny or ask
	// rule matched, nothing kiln cannot see while path rules exist, not a
	// plan-mode edit, and for bash a set of rules exists that allows the
	// line (DontAskRules). A prompt offers that option only when it is set;
	// the gate treats a PromptAllowAlways answer without it as PromptAllow.
	Grantable bool
	// DontAskRules are the bash rules (the text inside "Bash(…)") that
	// answer saves, one per command still needing approval
	// (settings.BashDontAskRules). Set only on a grantable bash request.
	DontAskRules []string
	// CallID is the tool call's id, and History returns the conversation
	// so far; both feed auto mode's classifier (classifier.go) and are
	// read only when it runs. Either may be empty.
	CallID  string
	History func() []msg.Message
	// Delegated and UserHistory are set for a subagent's call: History is
	// the subagent's own, whose user messages are its delegated task, and
	// UserHistory the root session's (ClassifyRequest).
	Delegated   bool
	UserHistory func() []msg.Message
	// AutoModeNote is set by the gate on a prompt auto mode raised instead
	// of deciding itself (the classifier failed, or blocked too often): why
	// the user is being asked. A UI shows it with the prompt.
	AutoModeNote string
	// InAutoMode is set by the gate on a prompt raised while auto mode is
	// on, so a UI does not offer to switch to the mode already active.
	InAutoMode bool
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
	Roots []string
	// ReadOnlyRoots are directories a read-only tool (settings.ReadOnly)
	// may read from without the extra "outside workspace" prompt, but
	// that a mutating tool (edit, write, bash, ...) still treats as
	// outside the workspace exactly as before: escaped-path handling for
	// those is unchanged. This is narrower than Roots on purpose — it
	// exists for Claude Code's auto-memory directory
	// (internal/claude/memory's LoadAutoMemory), which kiln reads but
	// must never write to.
	ReadOnlyRoots []string
	Mode          settings.PermissionMode
	Prompt        Prompter
	// PlanLedgerPath, when non-empty, is the one path plan mode's
	// read-only enforcement exempts: an edit/write call naming exactly
	// this path is allowed even in ModePlan. Empty leaves plan mode fully
	// read-only, as before. This is kiln's HARNESS_EXP_LEDGER experiment
	// switch (internal/cli/experiments.go) — small and easy to delete: it
	// touches only this field, its one check in CheckWithOutcome below,
	// and the call site that sets it.
	PlanLedgerPath string
	// SaveRule, when set, persists each bash allow rule a "Yes, and don't
	// ask again" answer grants (as Claude Code persists them; kiln writes
	// its own <cwd>/.kiln/settings.local.json, never .claude). The gate
	// adds the rule to its own set either way. Errors are the caller's to
	// report.
	SaveRule func(rule string)
	// Classifier judges what auto mode would otherwise allow
	// (classifier.go). Nil makes auto mode ask instead; it never allows.
	Classifier Classifier
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

	// sessionAllows are grants added by "yes, don't ask again" for a tool
	// other than bash, scoped to this session only, as Claude Code keeps
	// them. A bash grant is saved as rules instead (grant, saveRule).
	sessionAllows map[string]bool
	// saveRule persists a bash rule a "don't ask again" answer granted
	// (GateOptions.SaveRule); nil keeps it in this session only.
	saveRule func(rule string)

	// blockLog is everything refused this session, for diagnostics.
	blockLog []string

	// roots are directories tools may operate in freely. This is a real
	// boundary, not bookkeeping: without this check a model could read
	// ~/.ssh/id_rsa under an allow:[Read] rule the user only meant to apply
	// to their project.
	roots []string

	// readOnlyRoots mirrors GateOptions.ReadOnlyRoots; see its doc
	// comment. Checked only for tools in settings.ReadOnly, never for a
	// mutating one, so adding a directory here can only ever relax a
	// read, never a write.
	readOnlyRoots []string

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

	// classifier and auto are auto mode's classifier and its block counts
	// (classifier.go), guarded by mu.
	classifier Classifier
	auto       autoState
	// autoAsideLogged: the broad allow rules auto mode sets aside were
	// logged for this stretch of auto mode (rules, classifier.go).
	autoAsideLogged bool
	// scratchpad is the session scratchpad (scratchpad.go), guarded by mu.
	scratchpad []string
}

// NewGate builds a Gate. Roots are resolved to absolute paths and
// deduplicated.
func NewGate(opts GateOptions) *Gate {
	g := &Gate{
		permissions:   opts.Permissions,
		mode:          opts.Mode,
		prompter:      opts.Prompt,
		sessionAllows: map[string]bool{},
		saveRule:      opts.SaveRule,
		classifier:    opts.Classifier,
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
	for _, r := range opts.ReadOnlyRoots {
		g.AddReadOnlyRoot(r)
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
	return g.within(path, g.roots)
}

// AddReadOnlyRoot widens what a read-only tool may reach without asking,
// without widening what a mutating tool may reach. Returns the absolute
// path actually added. Like the workspace roots it is stored as given;
// WithinReadOnlyRoots resolves both sides. See GateOptions.ReadOnlyRoots.
func (g *Gate) AddReadOnlyRoot(dir string) string {
	full, err := filepath.Abs(dir)
	if err != nil {
		full = dir
	}
	for _, r := range g.readOnlyRoots {
		if r == full {
			return full
		}
	}
	g.readOnlyRoots = append(g.readOnlyRoots, full)
	return full
}

// ReadOnlyRoots returns the current read-only-only roots (not including
// the regular workspace roots, which are already read-write).
func (g *Gate) ReadOnlyRoots() []string {
	out := make([]string, len(g.readOnlyRoots))
	copy(out, g.readOnlyRoots)
	return out
}

// WithinReadOnlyRoots reports whether path lies inside a root added by
// AddReadOnlyRoot, resolved exactly as WithinRoots resolves a path. It does
// not consider the regular workspace roots: callers that want either kind
// check WithinRoots first, as Check does.
//
// Both the path as written and its real target must be inside the root, so
// a symlink planted in a read-only root (say `evil -> /etc/passwd` in Claude
// Code's auto-memory directory) does not lend its target the root's
// no-prompt treatment. Roots are resolved the same way, so /var vs
// /private/var spellings still match.
func (g *Gate) WithinReadOnlyRoots(path string) bool {
	return g.within(path, g.readOnlyRoots)
}

// within reports whether path, resolved against the first workspace root,
// lies inside one of roots both as written and through symlinks and the
// OS's own name for it (see WithinRoots).
func (g *Gate) within(path string, roots []string) bool {
	if len(roots) == 0 {
		return false
	}
	base := "."
	if len(g.roots) > 0 {
		base = g.roots[0]
	}
	full := execenv.ResolveToolPath(base, path)
	if !under(full, roots) {
		return false
	}
	real, ok := execenv.RealPath(full)
	if !ok {
		return false
	}
	realRoots := make([]string, 0, len(roots))
	canonRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		rr, _ := execenv.RealPath(r)
		realRoots = append(realRoots, rr)
		canonRoots = append(canonRoots, execenv.CanonicalPath(r))
	}
	// And the OS's own name for it (macOS firmlinks, APFS case folding,
	// /.vol inode paths), against the roots' own.
	return under(real, realRoots) && under(execenv.CanonicalPath(full), canonRoots)
}

// under reports whether full is one of roots or inside one. Paths compare
// as the filesystem compares them (settings.CaseFoldPath: without case on
// macOS and Windows), so "/Users/x/Proj/a" is inside a root "/Users/x/proj".
func under(full string, roots []string) bool {
	full = settings.CaseFoldPath(full)
	for _, root := range roots {
		rel, err := filepath.Rel(settings.CaseFoldPath(root), full)
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

// resolvePlanPath resolves a tool call's path argument the way the tools
// do (execenv.ResolveToolPath: "@", "~", "file://", relative to the first
// root), clean. Used only by the planLedgerPath exception, which needs to
// compare it against g.planLedgerPath (always absolute) whatever spelling
// the model used.
func (g *Gate) resolvePlanPath(path string) string {
	base := "."
	if len(g.roots) > 0 {
		base = g.roots[0]
	}
	return filepath.Clean(execenv.ResolveToolPath(base, path))
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

// SetRuleSaver binds GateOptions.SaveRule after construction, for a caller
// whose way of reporting a failed save exists only later.
func (g *Gate) SetRuleSaver(save func(rule string)) {
	g.mu.Lock()
	g.saveRule = save
	g.mu.Unlock()
}

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

// localSource is the source of a rule kiln saves ("don't ask again",
// /permissions): kiln's own <cwd>/.kiln/settings.local.json (kiln never
// writes Claude Code's .claude files), which anchors "/path" rules at the
// primary working directory.
func (g *Gate) localSource() settings.RuleSource {
	return settings.RuleSource{Scope: paths.ScopeLocal, File: paths.KilnLocalSettingsPath(g.cwd())}
}

// RuleOrigin names the settings file a rule in list came from, for a
// message about a rule kiln cannot delete; "the command line" for a flag
// or session rule.
func (g *Gate) RuleOrigin(list RuleList, rule string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, from := g.list(list)
	for i, r := range *l {
		if src := sourceAt(*from, i); r == rule && src.File != "" {
			return src.File
		}
	}
	return "the command line"
}

// AddSourcedRules adds rules read from a settings file after the gate was
// built (a .kiln file's allow rules held until the folder was trusted),
// each with its own source.
func (g *Gate) AddSourcedRules(list RuleList, rules []string, from []settings.RuleSource) {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, src := g.list(list)
	for len(*src) < len(*l) {
		*src = append(*src, settings.RuleSource{})
	}
	for i, r := range rules {
		*l = append(*l, r)
		*src = append(*src, sourceAt(from, i))
	}
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
//
// Keys are read exactly. That is sound only because the turn loop refuses
// input with case-variant keys before the gate runs (tool.CheckArgs in
// internal/harness beginTool): the tools decode case-insensitively.
func PathArgOf(args map[string]any) (string, bool) {
	for _, k := range tool.PathKeys {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok {
				return s, true
			}
		}
	}
	return "", false
}

// pathArgsOf returns every path-valued key a call carries, in
// tool.PathKeys order. A built-in tool declares at most one of them (and
// may not be sent the others: tool.CheckArgs refuses undeclared keys),
// but an MCP tool's input goes to a server that may read any of them, so
// the workspace check has to hold for all, not just the first.
func pathArgsOf(args map[string]any) []string {
	var out []string
	for _, k := range tool.PathKeys {
		if s, ok := args[k].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// PrimaryArgOf returns the identifying argument for a call, mirroring what
// the transcript shows. Permission rules match on this. Keys are read
// exactly; see PathArgOf.
func PrimaryArgOf(args map[string]any) (string, bool) {
	for _, k := range tool.PrimaryKeys {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok {
				return s, true
			}
		}
	}
	return "", false
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
	r, out, err := g.checkWithOutcome(ctx, req)
	// In auto mode, a call the user approved at a prompt ends a run of
	// classifier blocks; so does a classifier allow (classifyAuto). A call
	// that skipped the classifier does not: reads between blocks must not
	// keep the streak from ever reaching its limit.
	if r == nil && err == nil && out == OutcomeApproved && g.Mode() == settings.ModeAuto {
		g.autoSucceeded()
	}
	return r, out, err
}

func (g *Gate) checkWithOutcome(ctx context.Context, req Request) (*BlockResult, Outcome, error) {
	k := key(req.ToolName, req.PrimaryArg)

	permissions, mode := g.rules()

	// A file tool is judged on its path argument, resolved the way the
	// tool resolves it (settings/pathrules.go), whatever PrimaryArgOf
	// picked.
	decideArg := req.PrimaryArg
	if settings.IsFileTool(req.ToolName) {
		if p, ok := PathArgOf(req.Args); ok {
			decideArg = p
		}
	}
	hits := settings.RuleHits(permissions, g.cwd(), req.ToolName, decideArg)

	// The session scratchpad is the model's own: no prompt in any mode,
	// plan mode included, once deny and ask rules have had their say.
	if g.scratchpadCall(req, hits) {
		return nil, OutcomeAuto, nil
	}

	// A session "don't ask again" grant stands in for an allow rule, so
	// like one it never beats a deny or ask rule — including one added
	// after the grant (Claude Code: deny, then ask, then allow). Nor does
	// it cover a command naming a file kiln cannot resolve: the same text
	// ("cat $F") can name a different file next time.
	// Nor, while planning, does it cover an edit: plan mode refuses edits
	// whatever any allow says (settings.PlanOverridesAllow). A shell
	// command goes through the regular flow there, grants included.
	grantable := !hits.Deny && !hits.Ask && !hits.Unsure &&
		!(mode == settings.ModePlan && settings.PlanOverridesAllow(req.ToolName, decideArg))
	if grantable && g.sessionAllowed(k) {
		return nil, OutcomeAuto, nil
	}

	verdict := settings.DecideFromHits(hits, req.ToolName, decideArg, mode)

	// HARNESS_EXP_LEDGER (internal/cli/experiments.go): the one narrow
	// exception to plan mode's read-only enforcement — edit/write on
	// exactly the ledger path, nothing else, however the call spells it.
	// It is taken only after the rules have been evaluated, and only when
	// neither a deny nor an ask rule matched: a deny rule on the ledger
	// still refuses, and an ask rule still asks (rather than meeting plan
	// mode's refusal of edits). Every other tool and path keeps verdict.
	if mode == settings.ModePlan && g.planLedgerPath != "" && !hits.Deny &&
		(strings.EqualFold(req.ToolName, "edit") || strings.EqualFold(req.ToolName, "write")) {
		if path, ok := PathArgOf(req.Args); ok && g.resolvePlanPath(path) == g.planLedgerPath {
			if !hits.Ask {
				return nil, OutcomeAuto, nil
			}
			verdict = settings.Ask
		}
	}

	// A bash command that provably only reads, and only inside the
	// workspace, runs without asking in the modes that otherwise ask about
	// bash — the way the read tool never asks. Asking before "cat app.py"
	// or "git log" was pure friction. Rules still win: not when an ask rule
	// (a Bash rule, or a Read/Edit rule on a file it names) matched, or it
	// names a file kiln cannot resolve while path rules exist.
	if verdict == settings.Ask && strings.EqualFold(req.ToolName, "bash") &&
		(mode == settings.ModeManual || mode == settings.ModeAcceptEdits || mode == settings.ModeDontAsk) &&
		!hits.Ask && !hits.Unsure && hits.ReadOnly &&
		g.commandWithinRoots(req.PrimaryArg) {
		return nil, OutcomeAuto, nil
	}
	// Plan mode runs read-only commands without asking (Decide), but, as in
	// manual mode, only inside the workspace: one reading elsewhere asks.
	if verdict == settings.Allow && mode == settings.ModePlan && settings.IsBashTool(req.ToolName) &&
		!hits.Allow && hits.ReadOnly && !g.commandWithinRoots(req.PrimaryArg) {
		verdict = settings.Ask
	}

	// A path outside the workspace always warrants a question, even when a
	// rule would otherwise allow the tool. The one exception: a read-only
	// tool (settings.ReadOnly) whose path lies in a read-only root
	// (GateOptions.ReadOnlyRoots, e.g. Claude Code's auto-memory
	// directory) is not treated as escaped. A mutating tool's path is
	// never checked against readOnlyRoots, only against roots, so this
	// can only ever relax a read. Every path-valued key is checked, not
	// just the first (see pathArgsOf).
	var path string
	escaped := false
	for _, p := range pathArgsOf(req.Args) {
		if g.WithinRoots(p) || (settings.ReadOnly[strings.ToLower(req.ToolName)] && g.WithinReadOnlyRoots(p)) {
			continue
		}
		path, escaped = p, true
		break
	}
	if escaped && verdict == settings.Allow && mode != settings.ModeBypassPermissions {
		// Auto mode sends what would otherwise ask here to the classifier,
		// with the path marked as outside the workspace, as Claude Code's
		// auto mode does. A write to a protected path outside still asks.
		outsideNote := ""
		if mode == settings.ModeAuto && !(g.mutatingFileTool(req.ToolName) && g.protectedPath(path)) {
			creq := req
			creq.OutsideWorkspace = true
			r, out, note, err := g.classifyAuto(ctx, creq)
			if err != nil {
				return nil, OutcomeNone, err
			}
			if r != nil || out != OutcomeNone {
				return r, out, nil
			}
			outsideNote = note
		}
		if mode == settings.ModeDontAsk {
			r := g.record(req, fmt.Sprintf("%s is outside the workspace, and don't-ask mode refuses anything that would need approval.", path))
			return &r, OutcomeNone, nil
		}
		if g.prompter == nil {
			reason := fmt.Sprintf("%s is outside the workspace and cannot be confirmed.", path)
			if outsideNote != "" {
				reason = outsideNote + " " + reason
			}
			r := g.record(req, reason)
			return &r, OutcomeNone, nil
		}
		g.promptMu.Lock()
		defer g.promptMu.Unlock()
		if grantable && g.sessionAllowed(k) {
			return nil, OutcomeAuto, nil
		}
		promptReq := g.promptRequest(req, permissions, mode, grantable)
		promptReq.OutsideWorkspace = true
		promptReq.AutoModeNote = outsideNote
		choice, err := g.prompter(ctx, promptReq)
		if err != nil {
			return nil, OutcomeNone, err
		}
		if choice.Kind == PromptDeny {
			r := g.record(req, "the user declined access to a path outside the workspace.")
			return &r, OutcomeDeclined, nil
		}
		if choice.Kind == PromptAllowAlways && promptReq.Grantable {
			g.grant(k, promptReq)
		}
		return nil, OutcomeApproved, nil
	}

	// Auto mode: what rules and the boundary leave allowed goes past the
	// classifier, unless it is a call auto mode never classifies
	// (classifier.go). When the classifier cannot decide, or has blocked
	// too often, the call takes the ask path below, with a note saying why.
	autoNote := ""
	if verdict == settings.Allow && mode == settings.ModeAuto && settings.IsBashTool(req.ToolName) {
		if p := g.bashProtectedOutside(req.PrimaryArg); p != "" {
			verdict = settings.Ask
			autoNote = fmt.Sprintf("Auto mode asks before writing %s: it is a protected path outside the workspace.", p)
		}
	}
	if verdict == settings.Allow && mode == settings.ModeAuto && !g.autoSkipsClassifier(req, hits) {
		r, out, note, err := g.classifyAuto(ctx, req)
		if err != nil {
			return nil, OutcomeNone, err
		}
		if r != nil || out != OutcomeNone {
			return r, out, nil
		}
		verdict, autoNote = settings.Ask, note
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
		if mode == settings.ModePlan && !hits.Deny {
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
		reason := "requires confirmation and no prompt is available."
		if autoNote != "" {
			reason = autoNote + " " + reason
		}
		r := g.record(req, reason)
		return &r, OutcomeNone, nil
	}

	g.promptMu.Lock()
	defer g.promptMu.Unlock()
	if grantable && g.sessionAllowed(k) {
		return nil, OutcomeAuto, nil
	}
	// The rules may have changed while this call waited for another
	// prompt: a "don't ask again" there can have saved a rule covering
	// this command.
	permissions, mode = g.rules()
	if settings.IsBashTool(req.ToolName) {
		if h := settings.RuleHits(permissions, g.cwd(), req.ToolName, decideArg); h.Allow && !h.Deny && !h.Ask && !h.Unsure {
			return nil, OutcomeAuto, nil
		}
	}
	promptReq := g.promptRequest(req, permissions, mode, grantable)
	promptReq.AutoModeNote = autoNote
	choice, err := g.prompter(ctx, promptReq)
	if err != nil {
		return nil, OutcomeNone, err
	}
	if choice.Kind == PromptAllow || (choice.Kind == PromptAllowAlways && !promptReq.Grantable) {
		return nil, OutcomeApproved, nil
	}
	if choice.Kind == PromptAllowAlways {
		g.grant(k, promptReq)
		return nil, OutcomeApproved, nil
	}

	reason := "the user declined. Ask what they would prefer before trying again."
	if choice.Feedback != "" {
		reason = fmt.Sprintf("the user declined and said: %s", choice.Feedback)
	}
	r := g.record(req, reason)
	return &r, OutcomeDeclined, nil
}

// promptRequest is req as the prompt sees it: whether "don't ask again"
// would be honoured (grantable: no deny, ask or unsure hit, not a
// plan-mode edit), and for bash the rules it saves. A bash line is
// grantable only when settings.BashDontAskRules finds rules that, saved,
// let this exact call through in the current mode; otherwise the prompt
// would promise a grant the next identical call does not get.
func (g *Gate) promptRequest(req Request, permissions settings.Permissions, mode settings.PermissionMode, grantable bool) Request {
	req.Grantable, req.DontAskRules = grantable, nil
	req.InAutoMode = mode == settings.ModeAuto
	if !grantable || !settings.IsBashTool(req.ToolName) {
		return req
	}
	rules := settings.BashDontAskRules(permissions, g.cwd(), req.ToolName, req.PrimaryArg)
	if rules == nil {
		req.Grantable = false
		return req
	}
	with := permissions
	with.Allow = append(append([]string(nil), permissions.Allow...), settings.BashRules(rules)...)
	if settings.DecideFromHits(settings.RuleHits(with, g.cwd(), req.ToolName, req.PrimaryArg), req.ToolName, req.PrimaryArg, mode) != settings.Allow {
		req.Grantable = false
		return req
	}
	req.DontAskRules = rules
	return req
}

// grant applies a "don't ask again" answer to a grantable request. A bash
// line saves its rules (Claude Code: one per command still needing
// approval), as local-settings rules of this session and through
// saveRule; any other call is granted for this session, by tool and
// argument.
func (g *Gate) grant(k string, req Request) {
	if len(req.DontAskRules) == 0 {
		g.grantSession(k)
		return
	}
	g.mu.Lock()
	save := g.saveRule
	g.mu.Unlock()
	for _, r := range settings.BashRules(req.DontAskRules) {
		g.AddRule(RuleAllow, r)
		if save != nil {
			save(r)
		}
	}
}
