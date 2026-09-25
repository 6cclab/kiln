package tui

import (
	tea "charm.land/bubbletea/v2"
)

// trustIndent is the one-space left margin every Trust dialog body row
// sits at. Verified against testdata/reference/claude-code/dialog-trust.txt
// (all 11 rows carry exactly one leading space before their content, not
// the three-space dialogIndent the numbered-option dialogs use — see
// dialog_trust_test.go).
const trustIndent = " "

// trustDialog is the once-per-new-folder safety prompt, rendered full
// width starting with its own `─` rule (unlike the numbered-option
// dialogs, which sit under the app-owned `▔` rule and are indented three).
// Wording differs from Claude Code's own copy in two places per the task
// brief: "The harness'll be able to..." instead of "Claude Code'll be able
// to...", and no "Security guide" row (the harness has no such doc to
// link).
type trustDialog struct {
	cwd      string
	onAnswer func(trusted bool)
	cursor   int // 0 = "No, exit", 1 = "Yes, I trust this folder"
}

// NewTrustDialog builds the folder-trust Dialog. onAnswer is called once,
// right before the dialog closes, with the person's choice.
func NewTrustDialog(cwd string, onAnswer func(trusted bool)) Dialog {
	return &trustDialog{cwd: cwd, onAnswer: onAnswer}
}

const trustSafetyParagraph = "Quick safety check: Is this a project you created or one you trust? " +
	"(Like your own code, a well-known open source project, or work from your team). " +
	"If not, take a moment to review what's in this folder first."

func (d *trustDialog) Render(width, height int) []string {
	var out []string
	out = append(out, KilnAmber(rule('─', width)))
	out = append(out, trustIndent+KilnAmber(Bold("Accessing workspace:")))
	out = append(out, trustIndent+Ink(d.cwd))

	wrapWidth := width - len(trustIndent)
	if wrapWidth < 10 {
		wrapWidth = 10
	}
	for _, line := range wrapPlain(trustSafetyParagraph, wrapWidth) {
		out = append(out, trustIndent+Muted(line))
	}
	for _, line := range wrapPlain("kiln will be able to read, edit, and execute files here.", wrapWidth) {
		out = append(out, trustIndent+Muted(line))
	}

	options := []string{"No, exit", "Yes, I trust this folder"}
	for i, label := range options {
		marker := Faint("  ")
		text := Muted(label)
		selected := i == d.cursor
		if selected {
			marker = KilnAmber("❯ ")
			text = Ink(label)
		}
		row := trustIndent + marker + text
		if selected {
			row = OnRaise(padTo(row, width))
		}
		out = append(out, row)
	}

	out = append(out, trustIndent+Muted("Enter to confirm · Esc to cancel"))

	if height > 0 && len(out) > height {
		out = out[:height]
	}
	return out
}

// rule renders a full-width row of ch, matching the `─` top rule in
// dialog-trust.txt row 1 (100 columns in the reference capture).
func rule(ch rune, width int) string {
	if width < 0 {
		width = 0
	}
	b := make([]rune, width)
	for i := range b {
		b[i] = ch
	}
	return string(b)
}

func (d *trustDialog) HandleKey(msg tea.KeyPressMsg) (consumed, closeIt bool, cmd tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}
		return true, false, nil
	case "down", "j":
		if d.cursor < 1 {
			d.cursor++
		}
		return true, false, nil
	case "1":
		d.cursor = 0
		return d.answer(false)
	case "2":
		d.cursor = 1
		return d.answer(true)
	case "enter":
		return d.answer(d.cursor == 1)
	case "esc":
		return d.answer(false)
	}
	return true, false, nil
}

func (d *trustDialog) answer(trusted bool) (bool, bool, tea.Cmd) {
	if d.onAnswer != nil {
		d.onAnswer(trusted)
	}
	return true, true, nil
}
