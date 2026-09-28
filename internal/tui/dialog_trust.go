package tui

import (
	tea "charm.land/bubbletea/v2"
)

// trustDialog is the once-per-new-folder safety prompt. It used to render
// full width starting with its own `─` rule, on top of the app-owned `▔`
// rule every other dialog sat under (two stacked rules) — the kiln
// restyle drops trustDialog's own rule and its one-space indent
// (trustIndent) entirely: DialogTopRule now draws the single shared frame
// above every dialog, trustDialog included ("trust ────", FrameLabel
// below), and content aligns with every other block's left edge like the
// rest. Wording differs from Claude Code's own copy in two places per the
// task brief: "The harness'll be able to..." instead of "Claude Code'll be
// able to...", and no "Security guide" row (the harness has no such doc
// to link).
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

// FrameLabel names the label rule DialogTopRule draws above Trust.
func (d *trustDialog) FrameLabel() string { return "trust" }

func (d *trustDialog) Render(width, height int) []string {
	var out []string
	out = append(out, KilnAmber(Bold("Accessing workspace:")))
	out = append(out, Ink(d.cwd))

	wrapWidth := width
	if wrapWidth < 10 {
		wrapWidth = 10
	}
	for _, line := range wrapPlain(trustSafetyParagraph, wrapWidth) {
		out = append(out, Muted(line))
	}
	for _, line := range wrapPlain("kiln will be able to read, edit, and execute files here.", wrapWidth) {
		out = append(out, Muted(line))
	}

	options := []string{"No, exit", "Yes, I trust this folder"}
	for i, label := range options {
		selected := i == d.cursor
		marker := selectionGutter(selected)
		text := Muted(label)
		if selected {
			text = KilnAmber(label)
		}
		row := marker + text
		if selected {
			row = RaiseRow(row, width)
		}
		out = append(out, row)
	}

	out = append(out, Muted("Enter to confirm · Esc to cancel"))

	if height > 0 && len(out) > height {
		out = out[:height]
	}
	return out
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
