package permission

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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

	// sessionAllows are grants added by "yes, don't ask again", scoped to
	// this session only. Deliberately not persisted: a permission granted
	// in a hurry to unblock one task should not silently become permanent
	// policy.
	sessionAllows map[string]bool

	// blockLog is everything refused this session, for diagnostics.
	blockLog []string

	// roots are directories tools may operate in freely. This is a real
	// boundary, not bookkeeping: without this check a model could read
	// ~/.ssh/id_rsa under an allow:[Read] rule the user only meant to apply
	// to their project.
	roots []string
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
func (g *Gate) SetMode(mode settings.PermissionMode) { g.mode = mode }

// Mode returns the active permission mode.
func (g *Gate) Mode() settings.PermissionMode { return g.mode }

// SetPrompter binds the UI that asks the user. The gate is often
// constructed before the TUI, so the prompter arrives later rather than at
// construction.
func (g *Gate) SetPrompter(p Prompter) { g.prompter = p }

// Permissions returns the merged rules, for /permissions.
func (g *Gate) Permissions() settings.Permissions { return g.permissions }

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
	out := make([]string, 0, len(g.sessionAllows))
	for k := range g.sessionAllows {
		out = append(out, k)
	}
	return out
}

// Blocked returns the refusal log, for /permissions-style reporting.
func (g *Gate) Blocked() []string {
	out := make([]string, len(g.blockLog))
	copy(out, g.blockLog)
	return out
}

func (g *Gate) record(req Request, reason string) BlockResult {
	g.blockLog = append(g.blockLog, fmt.Sprintf("%s(%s): %s", req.ToolName, req.PrimaryArg, reason))
	return BlockResult{Reason: reason}
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

// Check decides, prompting if necessary. A nil result means proceed; a
// non-nil BlockResult carries the reason, written for the model.
func (g *Gate) Check(ctx context.Context, req Request) (*BlockResult, error) {
	if g.sessionAllows[key(req.ToolName, req.PrimaryArg)] {
		return nil, nil
	}

	verdict := settings.Decide(g.permissions, req.ToolName, req.PrimaryArg, g.mode)

	// A path outside the workspace always warrants a question, even when a
	// rule would otherwise allow the tool.
	path, hasPath := PathArgOf(req.Args)
	escaped := hasPath && !g.WithinRoots(path)
	if escaped && verdict == settings.Allow && g.mode != settings.ModeBypassPermissions {
		if g.prompter == nil {
			r := g.record(req, fmt.Sprintf("%s is outside the workspace and cannot be confirmed.", path))
			return &r, nil
		}
		promptReq := req
		promptReq.OutsideWorkspace = true
		choice, err := g.prompter(ctx, promptReq)
		if err != nil {
			return nil, err
		}
		if choice.Kind == PromptDeny {
			r := g.record(req, "the user declined access to a path outside the workspace.")
			return &r, nil
		}
		if choice.Kind == PromptAllowAlways {
			g.sessionAllows[key(req.ToolName, req.PrimaryArg)] = true
		}
		return nil, nil
	}

	if verdict == settings.Allow {
		return nil, nil
	}
	if verdict == settings.Deny {
		reason := "blocked by permission rules."
		if g.mode == settings.ModePlan {
			reason = fmt.Sprintf("plan mode is read-only, so %s is not available. Describe the change instead of making it.", req.ToolName)
		}
		r := g.record(req, reason)
		return &r, nil
	}

	// verdict == ask
	if g.prompter == nil {
		// Headless with no way to ask. Refusing beats proceeding: an
		// unattended run must not silently take an action the policy said
		// required confirmation.
		r := g.record(req, "requires confirmation and no prompt is available.")
		return &r, nil
	}

	choice, err := g.prompter(ctx, req)
	if err != nil {
		return nil, err
	}
	if choice.Kind == PromptAllow {
		return nil, nil
	}
	if choice.Kind == PromptAllowAlways {
		g.sessionAllows[key(req.ToolName, req.PrimaryArg)] = true
		return nil, nil
	}

	reason := "the user declined. Ask what they would prefer before trying again."
	if choice.Feedback != "" {
		reason = fmt.Sprintf("the user declined and said: %s", choice.Feedback)
	}
	r := g.record(req, reason)
	return &r, nil
}
