package permission

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/msg"
)

// Auto mode, as Claude Code's docs describe it
// (code.claude.com/docs/en/permission-modes, "Eliminate permission prompts
// with auto mode"): an action that rules and the mode would otherwise let
// through goes to a classifier model first. Deny rules, ask rules and the
// workspace boundary are settled before the classifier is consulted, so it
// can only narrow what auto mode allows, never widen it.
//
// What skips the classifier (autoSkipsClassifier): calls an allow rule or a
// session "don't ask again" grant already approves; read-only tools; edits
// and writes inside the workspace (a path outside it asks the user before
// this point); and a bash command that provably only reads, inside the
// workspace. Everything else — a mutating shell command, a web fetch, an
// MCP tool, a subagent dispatch — is classified. The docs' decision order
// ("How the classifier evaluates actions") lists the same steps.
//
// Seam for the bash sandbox: a sandboxed bash call that settings allow
// without asking (autoAllowBashIfSandboxed) is approved by that mechanism,
// not by the classifier, in Claude Code as well. It should reach this file
// as an Allow that autoSkipsClassifier reports as skipped — mark it on the
// Hits (as an allow) or extend autoSkipsClassifier; do not route it through
// classifyAuto.

// Classifier judges one action in auto mode. Implemented by
// internal/automode on top of a model; tests use fakes.
type Classifier interface {
	Classify(ctx context.Context, req ClassifyRequest) (Verdict, error)
}

// ClassifyRequest is one action for the classifier.
type ClassifyRequest struct {
	ToolName   string
	PrimaryArg string
	Args       map[string]any
	// CallID is the tool call's id, so the classifier can leave the call
	// being judged out of the history it is shown.
	CallID string
	// History is the conversation so far, raw. The Classifier decides what
	// of it the model may see (internal/automode's transcript boundary);
	// the gate passes it through untouched.
	History []msg.Message
	// Delegated marks History as a subagent's: its user messages are the
	// task the parent agent wrote, not the user's words. UserHistory is
	// then the root session's history, where the user's own lines are.
	Delegated   bool
	UserHistory []msg.Message
	// OutsideWorkspace marks an action on a path outside the workspace,
	// which in auto mode is the classifier's to judge; Workspace is the
	// workspace roots, so it can see where the path lies.
	OutsideWorkspace bool
	Workspace        []string
}

// Verdict is the classifier's answer.
type Verdict struct {
	Block bool
	// Reason is why, written for the model when Block is set.
	Reason string
}

// Repeated-block thresholds, Claude Code's (docs: permission-modes,
// "Repeated-block thresholds"): the block that makes 3 in a row, or 20 in
// the session, is shown to the user as a prompt instead of going back to
// the model. Approving it resets the streak; the total resets only when its
// own limit trips.
const (
	MaxConsecutiveBlocks = 3
	MaxTotalBlocks       = 20
)

// OutcomeClassifierBlocked means auto mode's classifier refused the call;
// the reason went back to the model as the tool result.
const OutcomeClassifierBlocked Outcome = "blocked by auto mode"

// errNoClassifier is what auto mode does without a classifier wired: ask,
// never allow.
var errNoClassifier = errors.New("no classifier is configured")

// autoState counts classifier blocks. Guarded by Gate.mu. Shared across
// every subagent, because they share the gate.
type autoState struct {
	consecutive int
	total       int
}

// SetClassifier binds the auto mode classifier. Without one, auto mode asks
// about everything it would have classified.
func (g *Gate) SetClassifier(c Classifier) {
	g.mu.Lock()
	g.classifier = c
	g.mu.Unlock()
}

// AutoBlocks reports the classifier's block counts (in a row, total), for
// diagnostics and tests.
func (g *Gate) AutoBlocks() (consecutive, total int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.auto.consecutive, g.auto.total
}

// rules is the rule set and mode a decision uses. In auto mode the broad
// allow rules (settings.IsBroadAutoModeAllow) are set aside, so what they
// would approve goes to the classifier; they apply again as soon as the
// mode changes, since nothing is removed from the gate's own rules.
// Claude Code drops them silently; kiln logs them to the run log, once per
// stretch of auto mode.
func (g *Gate) rules() (settings.Permissions, settings.PermissionMode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, mode := g.permissions, g.mode
	if mode != settings.ModeAuto {
		g.autoAsideLogged = false
		return p, mode
	}
	p, aside := settings.WithoutBroadAutoModeAllows(p)
	if len(aside) > 0 && !g.autoAsideLogged {
		g.autoAsideLogged = true
		diag.L().Info("auto mode sets aside broad allow rules", "rules", strings.Join(aside, ", "))
	}
	return p, mode
}

// autoSucceeded ends a run of blocks: a classifier allow, or a prompt the
// user approved in auto mode. A call that skipped the classifier does not.
func (g *Gate) autoSucceeded() {
	g.mu.Lock()
	g.auto.consecutive = 0
	g.mu.Unlock()
}

// autoSkipsClassifier reports a call that auto mode allows without asking
// the classifier. verdict is already Allow, and the workspace boundary has
// already been applied.
func (g *Gate) autoSkipsClassifier(req Request, hits settings.Hits) bool {
	name := strings.ToLower(req.ToolName)
	if settings.IsFileTool(name) && !settings.ReadOnly[name] {
		// A write to a protected path is classified even past an allow
		// rule; so is one whose path argument kiln cannot pin down.
		path, ok := soleEditPath(req.Args)
		if !ok || g.protectedPath(path) {
			return false
		}
	}
	if settings.IsBashTool(name) && g.bashTouchesProtected(req.PrimaryArg) {
		// Even past an allow rule or the read-only fast path: a redirect
		// into .git/config, git config, or a file kiln cannot name.
		return false
	}
	switch {
	case hits.Allow:
		return true
	case settings.ReadOnly[name]:
		return true
	case name == "edit" || name == "write":
		// Inside the workspace: an escaped path asked the user above.
		return true
	case settings.IsBashTool(name) && hits.ReadOnly && g.commandWithinRoots(req.PrimaryArg):
		return true
	}
	return false
}

// classifyAuto asks the classifier about req. It returns a non-nil
// BlockResult for a block, OutcomeAuto for an allow, and otherwise a note
// for the user: the gate then asks, as it would in manual mode. A cancelled
// ctx is returned as an error, as a cancelled prompt is.
//
// Fail closed: an error, a timeout, or an answer the classifier could not
// parse never allows — the call goes to the user (or, with nobody to ask,
// is refused). Those failures do not count as blocks.
func (g *Gate) classifyAuto(ctx context.Context, req Request) (*BlockResult, Outcome, string, error) {
	g.mu.Lock()
	c := g.classifier
	g.mu.Unlock()

	var history, userHistory []msg.Message
	if req.History != nil {
		history = req.History()
	}
	if req.UserHistory != nil {
		userHistory = req.UserHistory()
	}
	var verdict Verdict
	err := errNoClassifier
	if c != nil {
		verdict, err = c.Classify(ctx, ClassifyRequest{
			ToolName:         req.ToolName,
			PrimaryArg:       req.PrimaryArg,
			Args:             req.Args,
			CallID:           req.CallID,
			History:          history,
			Delegated:        req.Delegated,
			UserHistory:      userHistory,
			OutsideWorkspace: req.OutsideWorkspace,
			Workspace:        g.Roots(),
		})
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, OutcomeNone, "", ctxErr
	}
	if err != nil {
		return nil, OutcomeNone, fmt.Sprintf("Auto mode could not check this action (%s), so it needs your approval.", err), nil
	}
	if !verdict.Block {
		g.autoSucceeded()
		return nil, OutcomeAuto, "", nil
	}

	reason := strings.TrimSpace(verdict.Reason)
	if reason == "" {
		reason = "no reason given"
	}
	g.mu.Lock()
	g.auto.consecutive++
	g.auto.total++
	consecutive, total := g.auto.consecutive, g.auto.total
	if total >= MaxTotalBlocks {
		g.auto = autoState{}
	}
	g.mu.Unlock()

	switch {
	case total >= MaxTotalBlocks:
		return nil, OutcomeNone, fmt.Sprintf("Auto mode paused: %d actions were blocked this session. Latest block: %s", total, reason), nil
	case consecutive >= MaxConsecutiveBlocks:
		return nil, OutcomeNone, fmt.Sprintf("Auto mode paused: %d actions in a row were blocked. Latest block: %s", consecutive, reason), nil
	}
	r := g.record(req, AutoBlockMessage(reason))
	return &r, OutcomeClassifierBlocked, "", nil
}

// AutoBlockMessage is the tool result the model gets for a classifier
// block: the reason, and what to do instead of working around it.
func AutoBlockMessage(reason string) string {
	return "auto mode blocked this action: " + reason +
		". Do not try to reach the same result another way. If it is needed, explain why and ask the user to approve it or run it themselves."
}
