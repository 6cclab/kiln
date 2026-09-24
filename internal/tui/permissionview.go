package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// PromptState is the stateful half of the inline permission/plan prompt:
// the key handling and pending-reply plumbing that permission_render.go
// (the pure renderer) deliberately leaves out. Ported field-for-field from
// permission-prompt.ts's PermissionPromptView, key handling exactly as
// permission-prompt.ts:97-189.
//
// Parity spec §8: an inline block in the transcript flow, not a modal — a
// modal would hide the conversation the prompt is asking about.
type PromptState struct {
	// pending is set while a tool-permission prompt is up.
	pending *pendingPermission
	// plan is set while a plan is awaiting approval. Only one of pending
	// and plan is ever set.
	plan *pendingPlan
	// feedback is non-nil while the user is typing a reason after
	// choosing "no" (permission) or "3" (plan). "" is a valid, empty
	// in-progress feedback string; nil means not in feedback mode.
	feedback *string
	// cwd relativizes a path argument in the permission summary line.
	cwd string
}

type pendingPermission struct {
	request PermissionRequest
	reply   chan PromptChoice
}

type pendingPlan struct {
	plan  string
	reply chan PlanReply
}

// PromptChoice is the tool-permission answer, mirroring
// claude/permission.PromptChoice's shape (kept separate here so this
// package does not need to import claude/permission just for the type;
// the caller converts).
type PromptChoiceKind string

const (
	ChoiceAllow       PromptChoiceKind = "allow"
	ChoiceAllowAlways PromptChoiceKind = "allow-always"
	ChoiceDeny        PromptChoiceKind = "deny"
)

type PromptChoice struct {
	Kind     PromptChoiceKind
	Feedback string
}

// PlanReplyKind mirrors agent.PlanDecisionKind for the same reason.
type PlanReplyKind string

const (
	PlanApprove PlanReplyKind = "approve"
	PlanRevise  PlanReplyKind = "revise"
)

type PlanReply struct {
	Kind     PlanReplyKind
	Mode     string
	Feedback string
}

// NewPromptState builds an idle PromptState.
func NewPromptState(cwd string) *PromptState {
	return &PromptState{cwd: cwd}
}

// Active reports whether a prompt (tool permission or plan) is up.
func (p *PromptState) Active() bool {
	return p.pending != nil || p.plan != nil
}

// AskTool arms a tool-permission prompt. reply is buffered by 1 so the
// caller (the gate's prompter, blocked on <-reply) is released the instant
// HandleKey answers, with no risk of the send blocking should the caller
// have already given up.
func (p *PromptState) AskTool(req PermissionRequest) chan PromptChoice {
	reply := make(chan PromptChoice, 1)
	p.pending = &pendingPermission{request: req, reply: reply}
	p.feedback = nil
	return reply
}

// AskPlan arms a plan-approval prompt.
func (p *PromptState) AskPlan(plan string) chan PlanReply {
	reply := make(chan PlanReply, 1)
	p.plan = &pendingPlan{plan: plan, reply: reply}
	p.feedback = nil
	return reply
}

func (p *PromptState) finishTool(choice PromptChoice) {
	pending := p.pending
	p.pending = nil
	p.feedback = nil
	if pending != nil {
		pending.reply <- choice
	}
}

func (p *PromptState) finishPlan(reply PlanReply) {
	pending := p.plan
	p.plan = nil
	p.feedback = nil
	if pending != nil {
		pending.reply <- reply
	}
}

// HandleKey applies one key press to whichever prompt is active. It
// returns true when the key was consumed — the editor and the global
// router must not also act on it, or answering the prompt would leak
// keystrokes into the input box behind it.
//
// Precedence and behaviour match permission-prompt.ts exactly:
//   - 1/y/enter allow; 2/a allow-always; 3/n start feedback capture; esc
//     deny outright.
//   - Plan: 1/y/enter approve into acceptEdits; 2 approve into manual;
//     3/n/esc start feedback capture (a plan's "no" always asks why,
//     unlike a tool prompt's Esc, matching handlePlanKey).
//   - Feedback mode: printable runes append, backspace removes one,
//     enter finishes (deny-with-feedback for a tool prompt; revise, or
//     silently cancel back to the menu on empty text, for a plan), esc
//     cancels (deny outright for a tool prompt; back to the menu for a
//     plan).
//   - Anything else is swallowed while a prompt is up.
func (p *PromptState) HandleKey(msg tea.KeyPressMsg) bool {
	if p.plan != nil {
		return p.handlePlanKey(msg)
	}
	if p.pending == nil {
		return false
	}

	if p.feedback != nil {
		return p.handleToolFeedbackKey(msg)
	}

	switch strings.ToLower(msg.String()) {
	case "1", "y", "enter":
		p.finishTool(PromptChoice{Kind: ChoiceAllow})
		return true
	case "2", "a":
		p.finishTool(PromptChoice{Kind: ChoiceAllowAlways})
		return true
	case "3", "n":
		f := ""
		p.feedback = &f
		return true
	case "esc":
		p.finishTool(PromptChoice{Kind: ChoiceDeny})
		return true
	default:
		return true
	}
}

func (p *PromptState) handleToolFeedbackKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "enter":
		text := strings.TrimSpace(*p.feedback)
		p.finishTool(PromptChoice{Kind: ChoiceDeny, Feedback: text})
		return true
	case "backspace":
		f := *p.feedback
		if len(f) > 0 {
			// Drop one rune, not one byte, so multi-byte input edits
			// correctly.
			runes := []rune(f)
			f = string(runes[:len(runes)-1])
		}
		p.feedback = &f
		return true
	case "esc":
		p.finishTool(PromptChoice{Kind: ChoiceDeny})
		return true
	default:
		if text := msg.Text; text != "" {
			f := *p.feedback + text
			p.feedback = &f
		}
		return true
	}
}

func (p *PromptState) handlePlanKey(msg tea.KeyPressMsg) bool {
	if p.feedback != nil {
		return p.handlePlanFeedbackKey(msg)
	}

	switch strings.ToLower(msg.String()) {
	case "1", "y", "enter":
		p.finishPlan(PlanReply{Kind: PlanApprove, Mode: "acceptEdits"})
		return true
	case "2":
		p.finishPlan(PlanReply{Kind: PlanApprove, Mode: "manual"})
		return true
	case "3", "n", "esc":
		f := ""
		p.feedback = &f
		return true
	default:
		return true
	}
}

func (p *PromptState) handlePlanFeedbackKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "enter":
		text := strings.TrimSpace(*p.feedback)
		if text != "" {
			p.finishPlan(PlanReply{Kind: PlanRevise, Feedback: text})
		} else {
			// Empty feedback is not a revision request; back to the menu
			// rather than sending the model an empty instruction.
			p.feedback = nil
		}
		return true
	case "backspace":
		f := *p.feedback
		if len(f) > 0 {
			runes := []rune(f)
			f = string(runes[:len(runes)-1])
		}
		p.feedback = &f
		return true
	case "esc":
		p.feedback = nil
		return true
	default:
		if text := msg.Text; text != "" {
			f := *p.feedback + text
			p.feedback = &f
		}
		return true
	}
}

// Render renders whichever prompt is active, fitted to width. Empty when
// nothing is up.
func (p *PromptState) Render(width int) []string {
	if p.plan != nil {
		feedback := ""
		if p.feedback != nil {
			feedback = *p.feedback
		}
		return RenderPlanApproval(p.plan.plan, width, p.feedback != nil, feedback)
	}
	if p.pending == nil {
		return nil
	}
	feedback := ""
	if p.feedback != nil {
		feedback = *p.feedback
	}
	return RenderPermissionPrompt(p.pending.request, p.cwd, width, p.feedback != nil, feedback)
}
