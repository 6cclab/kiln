package tui

// Inline rendering for the ask_user_question prompt: kiln's port of Claude
// Code's AskUserQuestion tool UI. No design-handoff screen exists for it
// (docs/kiln-design-handoff/Terminal.dc.html has no question block), so
// this reuses the permission prompt's own visual language from that same
// file and permission_render.go: a "label rule" header, an amber rule,
// numbered options with the selected row raised (permissionOptionRow),
// and a dim "↑↓ select · ... · esc cancel" hint row — the same components
// RenderPermissionPrompt/RenderPlanApproval already build their prompts
// from. Every colour used here is one of theme.go's existing tokens
// (KilnAmber, Muted, Ink, Faint); none are new.

import (
	"fmt"
	"strings"
)

// AskUserOptionView is one option offered for a question, ready to render
// (already validated/parsed from the tool call's arguments).
type AskUserOptionView struct {
	Label       string
	Description string
}

// AskUserQuestionView is one question ready to render.
type AskUserQuestionView struct {
	Header      string
	Question    string
	Options     []AskUserOptionView
	MultiSelect bool
}

// renderQuestionTabs renders the tab strip / progress across questions:
// the current question's header raised in amber brackets, the others dim
// — shown only when there is more than one question (a single question
// has nothing to show progress against).
func renderQuestionTabs(headers []string, current int) string {
	parts := make([]string, len(headers))
	for i, h := range headers {
		if i == current {
			parts[i] = KilnAmber(Bold("[" + h + "]"))
		} else {
			parts[i] = Faint(h)
		}
	}
	return strings.Join(parts, "   ")
}

// askUserOptionLabel builds one option row's label text: "Label —
// description", prefixed with a checkbox glyph (gl.OK "✓" / gl.PlanTodo
// "○", the same two glyphs the "plan" block uses for done/pending) when
// the question is multi-select, so a toggled option reads the same way a
// completed plan item does. Single-select has no checkbox: only one
// option is ever "chosen", and choosing one always ends the question, so
// there is nothing separate to mark.
func askUserOptionLabel(opt AskUserOptionView, multiSelect, checked bool) string {
	label := opt.Label
	if opt.Description != "" {
		label += " — " + opt.Description
	}
	if !multiSelect {
		return label
	}
	gl := G()
	box := gl.PlanTodo
	if checked {
		box = gl.OK
	}
	return box + " " + label
}

// RenderAskUserQuestion renders one question of an ask_user_question
// exchange, fitted to width.
//
// headers is every question's header (for the tab strip), index this
// question's position. checked holds which of q.Options are toggled
// (multi-select only; ignored otherwise) by option index. otherMode is
// true while the user is typing a free-text "Other" answer instead of
// picking a listed option; otherText is what has been typed so far.
// selected is the highlighted row (0..len(q.Options), the last row being
// the automatic "Other" choice) when not in otherMode.
func RenderAskUserQuestion(headers []string, index int, q AskUserQuestionView, width, selected int, checked map[int]bool, otherMode bool, otherText string) []string {
	amberRule := KilnAmber(strings.Repeat(RuleFillChar(), maxInt(width, 1)))
	lines := []string{
		labelRule("question", KilnAmber, fmt.Sprintf("%d/%d", index+1, len(headers)), width),
		amberRule,
	}
	if len(headers) > 1 {
		lines = append(lines, " "+renderQuestionTabs(headers, index), "")
	}
	lines = append(lines, " "+KilnAmber(Bold(q.Question)), "")

	if otherMode {
		lines = append(lines,
			" "+Muted("Type your own answer:"),
			fmt.Sprintf(" %s %s%s", KilnAmber(">"), otherText, Faint("▌")),
			"",
			" "+Muted("enter to send · esc to cancel"),
			amberRule,
		)
		return FitLines(lines, width, "    ")
	}

	for i, opt := range q.Options {
		label := askUserOptionLabel(opt, q.MultiSelect, checked[i])
		key := fmt.Sprintf("%d", i+1)
		lines = append(lines, " "+permissionOptionRow(key, label, i == selected, maxInt(width-1, 1)))
	}
	otherIdx := len(q.Options)
	otherKey := fmt.Sprintf("%d", otherIdx+1)
	lines = append(lines, " "+permissionOptionRow(otherKey, "Other — type your own answer", otherIdx == selected, maxInt(width-1, 1)))

	hint := "↑↓ select · enter confirm · esc cancel"
	if q.MultiSelect {
		hint = "↑↓ select · space toggle · enter confirm · esc cancel"
	}
	lines = append(lines, "", " "+Muted(hint), amberRule)
	return FitLines(lines, width, "    ")
}

// RenderAskUserReview renders the review screen shown after the last
// question of a multi-question exchange: every question with its chosen
// answer(s), then "Submit answers" / "Cancel". Never shown for a single
// question — answering it ends the exchange directly.
func RenderAskUserReview(questions []AskUserQuestionView, answers [][]string, width, selected int) []string {
	amberRule := KilnAmber(strings.Repeat(RuleFillChar(), maxInt(width, 1)))
	lines := []string{
		labelRule("review answers", KilnAmber, "", width),
		amberRule,
		" " + KilnAmber(Bold("Review your answers")),
		"",
	}
	for i, q := range questions {
		lines = append(lines, " "+Muted(q.Question))
		ans := "—"
		if i < len(answers) && len(answers[i]) > 0 {
			ans = strings.Join(answers[i], ", ")
		}
		lines = append(lines, "   "+Ink(ans), "")
	}

	opts := []string{"Submit answers", "Cancel"}
	for i, opt := range opts {
		key := fmt.Sprintf("%d", i+1)
		lines = append(lines, " "+permissionOptionRow(key, opt, i == selected, maxInt(width-1, 1)))
	}
	lines = append(lines, "", " "+Muted("↑↓ select · enter confirm · esc cancel"), amberRule)
	return FitLines(lines, width, "    ")
}
