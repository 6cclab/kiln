package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/tools"
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
	// question is set while an ask_user_question exchange is up. Only one
	// of pending, plan and question is ever set.
	question *pendingQuestion
	// feedback is non-nil while the user is typing a reason after
	// choosing "no" (permission) or "3" (plan). "" is a valid, empty
	// in-progress feedback string; nil means not in feedback mode.
	feedback *string
	// cwd relativizes a path argument in the permission summary line.
	cwd string
	// lastDenied is set by finishTool when a tool-permission prompt is
	// answered with ChoiceDeny (Esc, or "n"/"3" then Enter on a — possibly
	// empty — feedback string), and cleared by the caller once it has
	// committed the "✕ Declined …" note (app.go's handleKey, right after
	// routing the key that closed the prompt). It carries the denied
	// request rather than a pre-rendered string so app.go can format the
	// note the same way RenderToolCall would name the call ("bash npm
	// test -- upload", "Update src/math.js").
	lastDenied *PermissionRequest
	// lastDeniedFeedback is what the user typed with that "no", shown on
	// the same note.
	lastDeniedFeedback string
	// switchMode is set by chooseOption when the option picked is "switch
	// to <mode> then allow" (Bash's "switch to auto mode", Edit/Write's
	// "switch to accept edits"). app.go's handleKey reads it right after
	// routing the key that set it (same pattern as lastDenied), applies
	// it to the real permission gate (which PromptState has no reference
	// to), and clears it.
	switchMode string
}

// promptOptionKind names what pressing one option row does, independent of
// its rendered label or position — the same four actions
// permission_render.go's option lists are built from (allow / allow-always
// / switch-mode-then-allow / deny), just per-variant which ones exist and
// in what order.
type promptOptionKind int

const (
	optAllow promptOptionKind = iota
	optAllowAlways
	optSwitchAutoAllow
	optSwitchAcceptEditsAllow
	optDenyFeedback
	optDenyOutright
)

// promptOptionsFor returns the option list for a tool-permission prompt,
// in the exact order RenderBashPermissionPrompt/RenderEditPermissionPrompt/
// RenderPermissionPrompt render them, so a key index (1..N), ↑/↓, and Esc
// all resolve to the same action the rendered row promises.
//
// "Don't ask again" is listed only when offersDontAsk: without it the rows
// below move up a number, as in Claude Code, and its "a" shortcut does
// nothing.
func promptOptionsFor(req PermissionRequest) []promptOptionKind {
	var opts []promptOptionKind
	switch strings.ToLower(req.ToolName) {
	case "bash":
		// RenderBashPermissionPrompt: Yes / don't-ask-again / switch to
		// auto mode / No.
		opts = []promptOptionKind{optAllow, optAllowAlways, optSwitchAutoAllow, optDenyOutright}
	case "edit", "write":
		// RenderEditPermissionPrompt: Yes / switch to accept edits / No.
		return []promptOptionKind{optAllow, optSwitchAcceptEditsAllow, optDenyOutright}
	default:
		// RenderPermissionPrompt: Yes / don't-ask-again / No-and-tell-kiln.
		opts = []promptOptionKind{optAllow, optAllowAlways, optDenyFeedback}
	}
	if !offersDontAsk(req) {
		opts = append(opts[:1:1], opts[2:]...)
	}
	return opts
}

// offersDontAsk reports a request whose prompt lists "don't ask again":
// one the gate marked Grantable (it would honour the grant) and, for
// bash, with the rules that answer saves, since the row names them.
func offersDontAsk(req PermissionRequest) bool {
	if strings.EqualFold(req.ToolName, "bash") {
		return req.Grantable && len(req.DontAskRules) > 0
	}
	return req.Grantable
}

type pendingPermission struct {
	request  PermissionRequest
	reply    chan PromptChoice
	selected int // 0..len(promptOptionsFor(request))-1
}

type pendingPlan struct {
	plan     string
	path     string
	reply    chan PlanReply
	selected int // 0..2, the highlighted option row
}

// pendingQuestion is one ask_user_question exchange in progress: one or
// more questions, answered one at a time (askuser_render.go's tab strip),
// ending either directly (a single question) or via a review screen (more
// than one) before the reply is sent.
type pendingQuestion struct {
	questions []tools.AskUserQuestion
	// index is the question currently on screen; answers[i] is filled in
	// once question i is answered, nil until then.
	index   int
	answers [][]string
	// selected is the highlighted option row (0..len(Options), the last
	// being the automatic "Other" choice) for the current question.
	selected int
	// checked holds which of the current question's options are toggled,
	// multi-select only; reset to {} on every question change.
	checked map[int]bool
	// otherText is non-nil while typing a free-text "Other" answer for the
	// current question.
	otherText *string
	// reviewing is true once every question has an answer and there was
	// more than one: the review screen lists them all before submitting.
	reviewing      bool
	reviewSelected int // 0 = submit, 1 = cancel
	reply          chan AskUserReply
}

// AskUserReply is the answer to an ask_user_question exchange: either
// every question's answers, in order, or Cancelled if the user declined
// (Esc from a question, or "Cancel" from the review screen).
type AskUserReply struct {
	Cancelled bool
	Answers   []tools.AskUserAnswer
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

// Active reports whether a prompt (tool permission, plan or question) is up.
func (p *PromptState) Active() bool {
	return p.pending != nil || p.plan != nil || p.question != nil
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

// AskPlan arms a plan-approval prompt. path is the plan file's location
// (deliverable 7: the harness writes it to ~/.harness/plans/<slug>.md),
// shown on the prompt's last row.
func (p *PromptState) AskPlan(plan, path string) chan PlanReply {
	reply := make(chan PlanReply, 1)
	p.plan = &pendingPlan{plan: plan, path: path, reply: reply}
	p.feedback = nil
	return reply
}

// AskAskUser arms an ask_user_question prompt, the same blocking-reply
// contract as AskTool/AskPlan.
func (p *PromptState) AskAskUser(questions []tools.AskUserQuestion) chan AskUserReply {
	reply := make(chan AskUserReply, 1)
	p.question = &pendingQuestion{questions: questions, checked: map[int]bool{}, reply: reply}
	p.feedback = nil
	return reply
}

func (p *PromptState) finishTool(choice PromptChoice) {
	pending := p.pending
	p.pending = nil
	p.feedback = nil
	if pending != nil {
		if choice.Kind == ChoiceDeny {
			req := pending.request
			p.lastDenied = &req
			p.lastDeniedFeedback = strings.TrimSpace(choice.Feedback)
		}
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

// cancelQuestion ends the whole ask_user_question exchange as declined —
// Esc from any question, or "Cancel" on the review screen.
func (p *PromptState) cancelQuestion() {
	pending := p.question
	p.question = nil
	p.feedback = nil
	if pending != nil {
		pending.reply <- AskUserReply{Cancelled: true}
	}
}

// submitQuestion ends the exchange successfully, converting each
// question's recorded answer into the tools.AskUserAnswer shape the
// approver returns.
func (p *PromptState) submitQuestion() {
	pending := p.question
	p.question = nil
	p.feedback = nil
	if pending == nil {
		return
	}
	answers := make([]tools.AskUserAnswer, len(pending.questions))
	for i, q := range pending.questions {
		answers[i] = tools.AskUserAnswer{Question: q.Question, Answers: pending.answers[i]}
	}
	pending.reply <- AskUserReply{Answers: answers}
}

// recordAnswer stores the current question's answer and either advances
// to the next question, opens the review screen (more than one question,
// all now answered), or submits directly (a single question needs no
// review — answering it IS finishing the exchange).
func (p *PromptState) recordAnswer(answer []string) {
	q := p.question
	if q.answers == nil {
		q.answers = make([][]string, len(q.questions))
	}
	q.answers[q.index] = answer
	if q.index == len(q.questions)-1 {
		if len(q.questions) == 1 {
			p.submitQuestion()
			return
		}
		q.reviewing = true
		q.reviewSelected = 0
		return
	}
	q.index++
	q.selected = 0
	q.checked = map[int]bool{}
	q.otherText = nil
}

// chooseQuestionOption applies picking option i (0..len(Options)-1) or the
// automatic "Other" row (i == len(Options)) on a single-select question:
// Other opens free-text entry, anything else answers and advances
// immediately, matching a single-select permission option's own
// pick-and-commit behaviour. Multi-select questions never call this for a
// plain pick — see handleQuestionKey's "space"/digit toggling and its
// "enter" case's own confirm-the-checked-set logic.
func (p *PromptState) chooseQuestionOption(i int) {
	q := p.question
	current := q.questions[q.index]
	if i == len(current.Options) {
		t := ""
		q.otherText = &t
		return
	}
	p.recordAnswer([]string{current.Options[i].Label})
}

func (p *PromptState) handleQuestionKey(msg tea.KeyPressMsg) bool {
	q := p.question
	if q.reviewing {
		return p.handleReviewKey(msg)
	}
	if q.otherText != nil {
		return p.handleOtherKey(msg)
	}

	current := q.questions[q.index]
	total := len(current.Options) + 1 // + the automatic "Other" row

	switch strings.ToLower(msg.String()) {
	case "up", "k":
		if q.selected > 0 {
			q.selected--
		}
		return true
	case "down", "j":
		if q.selected < total-1 {
			q.selected++
		}
		return true
	case "esc":
		p.cancelQuestion()
		return true
	case " ", "space":
		if current.MultiSelect && q.selected < len(current.Options) {
			q.checked[q.selected] = !q.checked[q.selected]
		}
		return true
	case "enter":
		if current.MultiSelect {
			if q.selected == len(current.Options) {
				t := ""
				q.otherText = &t
				return true
			}
			var chosen []string
			for i, opt := range current.Options {
				if q.checked[i] {
					chosen = append(chosen, opt.Label)
				}
			}
			if len(chosen) == 0 {
				// Nothing picked yet; Enter with no selection is a no-op
				// rather than sending an empty answer.
				return true
			}
			p.recordAnswer(chosen)
			return true
		}
		p.chooseQuestionOption(q.selected)
		return true
	default:
		if idx := digitIndex(msg.String()); idx >= 1 && idx <= total {
			q.selected = idx - 1
			if current.MultiSelect {
				if q.selected < len(current.Options) {
					q.checked[q.selected] = !q.checked[q.selected]
				}
			} else {
				p.chooseQuestionOption(q.selected)
			}
		}
		return true
	}
}

func (p *PromptState) handleOtherKey(msg tea.KeyPressMsg) bool {
	q := p.question
	switch msg.String() {
	case "enter":
		text := strings.TrimSpace(*q.otherText)
		if text == "" {
			// Empty free text is not an answer; back to the option list.
			q.otherText = nil
			return true
		}
		q.otherText = nil
		current := q.questions[q.index]
		if current.MultiSelect {
			var chosen []string
			for i, opt := range current.Options {
				if q.checked[i] {
					chosen = append(chosen, opt.Label)
				}
			}
			chosen = append(chosen, text)
			p.recordAnswer(chosen)
		} else {
			p.recordAnswer([]string{text})
		}
		return true
	case "backspace":
		f := *q.otherText
		if len(f) > 0 {
			runes := []rune(f)
			f = string(runes[:len(runes)-1])
		}
		q.otherText = &f
		return true
	case "esc":
		p.cancelQuestion()
		return true
	default:
		if text := msg.Text; text != "" {
			f := *q.otherText + text
			q.otherText = &f
		}
		return true
	}
}

func (p *PromptState) handleReviewKey(msg tea.KeyPressMsg) bool {
	q := p.question
	switch strings.ToLower(msg.String()) {
	case "up", "down", "k", "j":
		q.reviewSelected = 1 - q.reviewSelected
		return true
	case "enter":
		if q.reviewSelected == 0 {
			p.submitQuestion()
		} else {
			p.cancelQuestion()
		}
		return true
	case "esc":
		p.cancelQuestion()
		return true
	case "1":
		p.submitQuestion()
		return true
	case "2":
		p.cancelQuestion()
		return true
	default:
		return true
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
//   - tab opens the same inline feedback field as "3/n" on a prompt
//     variant that has no numbered feedback option of its own (Bash's
//     four options are allow/allow-always/switch-then-allow/deny-
//     outright; Edit/Write's three are allow/switch-then-allow/deny-
//     outright — neither lists a "tell kiln what to do instead" option,
//     yet both render the hint row "... · tab to amend"
//     (permission_render.go's RenderBashPermissionPrompt/
//     RenderEditPermissionPrompt). Before this, tab fell through to the
//     digit-key default case, which does nothing for a non-digit key —
//     the prompt silently ate the keystroke, subsequent typed characters
//     leaked into the editor behind it, and Enter ran whatever option was
//     already highlighted (finding tab-to-amend-not-implemented). Tab
//     works from any selected option, not just "No": it is a shortcut to
//     "decline with a reason" independent of the highlighted row, mirroring
//     Claude Code's own tab-to-amend and this same package's shift+tab on
//     the plan-approval prompt (handlePlanKey's "shift+tab" case below).
//     It is a no-op for the generic prompt (RenderPermissionPrompt),
//     which already has its own numbered feedback option ("3/n"); tab
//     there takes the same feedback-capture path via optDenyFeedback so
//     the two entry points converge on identical behaviour.
//   - Plan: 1/y/enter approve into acceptEdits; 2 approve into manual;
//     3/n/esc start feedback capture (a plan's "no" always asks why,
//     unlike a tool prompt's Esc, matching handlePlanKey).
//   - Feedback mode: printable runes append, backspace removes one,
//     enter finishes (deny-with-feedback for a tool prompt; revise, or
//     silently cancel back to the menu on empty text, for a plan), esc
//     cancels (deny outright for a tool prompt; back to the menu for a
//     plan).
//   - Anything else is swallowed while a prompt is up.
//
// Paste appends pasted text to the reason being typed after a "no", and
// reports whether the prompt took it. Outside feedback mode a prompt has no
// text field, so the paste is not the prompt's to take.
func (p *PromptState) Paste(text string) bool {
	if p.question != nil && p.question.otherText != nil {
		f := *p.question.otherText + strings.ReplaceAll(text, "\r\n", "\n")
		p.question.otherText = &f
		return true
	}
	if p.feedback == nil {
		return false
	}
	f := *p.feedback + strings.ReplaceAll(text, "\r\n", "\n")
	p.feedback = &f
	return true
}

func (p *PromptState) HandleKey(msg tea.KeyPressMsg) bool {
	if p.question != nil {
		return p.handleQuestionKey(msg)
	}
	if p.plan != nil {
		return p.handlePlanKey(msg)
	}
	if p.pending == nil {
		return false
	}

	if p.feedback != nil {
		return p.handleToolFeedbackKey(msg)
	}

	opts := promptOptionsFor(p.pending.request)

	switch strings.ToLower(msg.String()) {
	case "up", "k":
		if p.pending.selected > 0 {
			p.pending.selected--
		}
		return true
	case "down", "j":
		if p.pending.selected < len(opts)-1 {
			p.pending.selected++
		}
		return true
	case "enter":
		p.chooseOption(opts[p.pending.selected])
		return true
	case "tab":
		f := ""
		p.feedback = &f
		return true
	case "esc":
		p.finishTool(PromptChoice{Kind: ChoiceDeny})
		return true
	case "y":
		p.chooseOption(optAllow)
		return true
	case "a":
		if idx := indexOfOption(opts, optAllowAlways); idx >= 0 {
			p.chooseOption(opts[idx])
			return true
		}
		return true
	case "n":
		if idx := indexOfOption(opts, optDenyFeedback); idx >= 0 {
			p.chooseOption(opts[idx])
			return true
		}
		p.finishTool(PromptChoice{Kind: ChoiceDeny})
		return true
	default:
		if idx := digitIndex(msg.String()); idx >= 1 && idx <= len(opts) {
			p.chooseOption(opts[idx-1])
			return true
		}
		return true
	}
}

// chooseOption applies whichever option kind a key resolved to: it always
// ends the prompt (finishTool) except for optDenyFeedback, which opens the
// feedback field instead, matching every rendered option's actual effect
// (permission_render.go's option lists).
func (p *PromptState) chooseOption(opt promptOptionKind) {
	switch opt {
	case optAllow:
		p.finishTool(PromptChoice{Kind: ChoiceAllow})
	case optAllowAlways:
		p.finishTool(PromptChoice{Kind: ChoiceAllowAlways})
	case optSwitchAutoAllow:
		p.switchMode = "auto"
		p.finishTool(PromptChoice{Kind: ChoiceAllow})
	case optSwitchAcceptEditsAllow:
		p.switchMode = "acceptEdits"
		p.finishTool(PromptChoice{Kind: ChoiceAllow})
	case optDenyFeedback:
		f := ""
		p.feedback = &f
	case optDenyOutright:
		p.finishTool(PromptChoice{Kind: ChoiceDeny})
	}
}

func indexOfOption(opts []promptOptionKind, want promptOptionKind) int {
	for i, o := range opts {
		if o == want {
			return i
		}
	}
	return -1
}

// digitIndex parses a single-digit key ("1".."9") into its 1-based index,
// or -1 for anything else — msg.String() for a digit key is exactly that
// digit, so no need for strconv's full error handling.
func digitIndex(s string) int {
	if len(s) != 1 || s[0] < '1' || s[0] > '9' {
		return -1
	}
	return int(s[0] - '0')
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
	case "up", "k":
		if p.plan.selected > 0 {
			p.plan.selected--
		}
		return true
	case "down", "j":
		if p.plan.selected < 2 {
			p.plan.selected++
		}
		return true
	case "1", "y":
		// "Yes, and use auto mode": auto, as the option says. acceptEdits
		// still prompted for every command the plan went on to run.
		p.finishPlan(PlanReply{Kind: PlanApprove, Mode: "auto"})
		return true
	case "2":
		p.finishPlan(PlanReply{Kind: PlanApprove, Mode: "manual"})
		return true
	case "3":
		// Move to "Tell the model what to change"; shift+tab (or enter on
		// it) opens the feedback field (plan-keep-planning.txt: ❯ on 3,
		// no field yet).
		p.plan.selected = 2
		return true
	case "n", "esc":
		f := ""
		p.feedback = &f
		return true
	case "shift+tab":
		if p.plan.selected == 2 {
			f := ""
			p.feedback = &f
		}
		return true
	case "enter":
		switch p.plan.selected {
		case 0:
			p.finishPlan(PlanReply{Kind: PlanApprove, Mode: "auto"})
		case 1:
			p.finishPlan(PlanReply{Kind: PlanApprove, Mode: "manual"})
		case 2:
			f := ""
			p.feedback = &f
		}
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
//
// This keeps app.go's existing single-argument call site working
// (app.go:810, m.prompt.Render(width) — outside this pass's scope to
// edit). RenderPlanApproval also accepts a height for its vertical-scroll
// clipping (see its doc comment); this method always passes 0
// (unbounded) since app.go does not thread a height through yet. Whoever
// wires the plan-approval prompt into app.go for real should call
// RenderPlanApproval directly with a real height, or this method should
// grow a second (width, height int) form once app.go's call site can pass
// one — flagged in the handback report rather than done silently.
func (p *PromptState) Render(width int) []string {
	if p.question != nil {
		return p.renderQuestion(width)
	}
	if p.plan != nil {
		feedback := ""
		if p.feedback != nil {
			feedback = *p.feedback
		}
		return RenderPlanApproval(p.plan.plan, p.plan.path, width, 0, p.plan.selected, p.feedback != nil, feedback)
	}
	if p.pending == nil {
		return nil
	}
	feedback := ""
	if p.feedback != nil {
		feedback = *p.feedback
	}

	req := p.pending.request
	switch strings.ToLower(req.ToolName) {
	case "bash":
		cmd, _ := req.Args["command"].(string)
		if cmd == "" {
			cmd = req.PrimaryArg
		}
		desc, _ := req.Args["description"].(string)
		// The feedback field replaces the options inside the same bash
		// prompt, so tab does not swap in the generic prompt's different
		// title, indent and spacing.
		var fb *string
		if p.feedback != nil {
			fb = &feedback
		}
		return RenderBashPermissionPrompt(BashPermissionRequest{Command: cmd, Description: desc, Feedback: fb,
			Grantable: req.Grantable, DontAskRules: req.DontAskRules}, width, p.pending.selected)
	case "edit":
		return RenderEditPermissionPrompt(EditPermissionRequest{
			Kind:  EditKindEdit,
			Path:  SummarizeArg(req, p.cwd),
			Hunks: diffHunksFromEditFile(p.cwd, req.Args),
		}, width, p.pending.selected, p.feedback != nil, feedback)
	case "write":
		return RenderEditPermissionPrompt(EditPermissionRequest{
			Kind:  EditKindWrite,
			Path:  SummarizeArg(req, p.cwd),
			Hunks: diffHunksFromWriteArgs(req.Args),
		}, width, p.pending.selected, p.feedback != nil, feedback)
	default:
		return RenderPermissionPrompt(req, p.cwd, width, p.pending.selected, p.feedback != nil, feedback)
	}
}

// renderQuestion renders whichever screen the current ask_user_question
// exchange is on: the review screen once every question is answered (only
// shown for more than one question), otherwise the question currently on
// screen.
func (p *PromptState) renderQuestion(width int) []string {
	q := p.question
	headers := make([]string, len(q.questions))
	views := make([]AskUserQuestionView, len(q.questions))
	for i, qq := range q.questions {
		headers[i] = qq.Header
		views[i] = toAskUserQuestionView(qq)
	}
	if q.reviewing {
		return RenderAskUserReview(views, q.answers, width, q.reviewSelected)
	}
	otherText := ""
	if q.otherText != nil {
		otherText = *q.otherText
	}
	return RenderAskUserQuestion(headers, q.index, views[q.index], width, q.selected, q.checked, q.otherText != nil, otherText)
}

func toAskUserQuestionView(q tools.AskUserQuestion) AskUserQuestionView {
	opts := make([]AskUserOptionView, len(q.Options))
	for i, o := range q.Options {
		opts[i] = AskUserOptionView{Label: o.Label, Description: o.Description}
	}
	return AskUserQuestionView{Header: q.Header, Question: q.Question, Options: opts, MultiSelect: q.MultiSelect}
}
