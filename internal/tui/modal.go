package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// ModalView is the panel host for commands.ModalSpec — the Go analogue of
// modal.ts's ModalView/createModalHost, simplified because
// commands.ModalSpec is synchronous and its Items are a plain slice
// rather than a lazily-refetched async list: there is no need for
// pi-tui's SelectList or an overlay manager here, just a small hand-rolled
// cursor list spliced over the live region by the caller (app.go).
//
// Rendered as a bordered box because it is *modal* — it takes the
// keyboard, and looking like part of the transcript while swallowing
// every key is how a UI feels broken. The border is the signal that Esc
// is what gets you out.
type ModalView struct {
	spec     commands.ModalSpec
	cursor   int
	status   string
	statusOK bool
}

// NewModalView builds a ModalView over spec.
func NewModalView(spec commands.ModalSpec) *ModalView {
	return &ModalView{spec: spec}
}

// HandleKey applies one key to the modal. Returns (consumed, closeRequested).
//
//   - Esc closes (checked first, so it always means "leave" even if an
//     action happens to be bound to "esc" — none are, but the order
//     matches modal.ts's own precedence).
//   - Up/Down/j/k move the cursor.
//   - Enter runs Select against the selected item's value.
//   - Any key matching an Action's key (case-insensitively) runs Act.
//   - Everything else is swallowed: a modal owns the keyboard.
func (m *ModalView) HandleKey(msg tea.KeyPressMsg) (consumed, shouldClose bool) {
	key := msg.String()
	if key == "esc" {
		return true, true
	}

	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return true, false
	case "down", "j":
		if m.cursor < len(m.spec.Items)-1 {
			m.cursor++
		}
		return true, false
	case "enter":
		if m.spec.Select != nil {
			if item, ok := m.selected(); ok {
				msg, err := m.spec.Select(item.Value)
				m.setStatus(msg, err)
			}
		}
		return true, false
	}

	for _, action := range m.spec.Actions {
		if strings.EqualFold(action.Key, key) {
			if m.spec.Act != nil {
				value := ""
				if item, ok := m.selected(); ok {
					value = item.Value
				}
				msg, err := m.spec.Act(action.Key, value)
				m.setStatus(msg, err)
			}
			return true, false
		}
	}

	// Swallow everything else; a modal owns the keyboard while it is up.
	return true, false
}

func (m *ModalView) selected() (commands.Item, bool) {
	if m.cursor < 0 || m.cursor >= len(m.spec.Items) {
		return commands.Item{}, false
	}
	return m.spec.Items[m.cursor], true
}

func (m *ModalView) setStatus(msg string, err error) {
	if err != nil {
		m.status = err.Error()
		m.statusOK = false
		return
	}
	m.status = msg
	m.statusOK = true
}

// Render draws the panel at the given width/height (the composite caller
// already sized it to 80%/80% of the live region — see app.go). Rows
// beyond height are clipped, matching a bordered, fixed-size overlay
// rather than a scrolling one; the item list itself has a cursor and the
// list is short in every panel this ships with.
func (m *ModalView) Render(width, height int) []string {
	inner := width - 2
	if inner < 10 {
		inner = 10
	}
	var lines []string
	lines = append(lines, Bold(m.spec.Title))
	lines = append(lines, Gray(strings.Repeat("─", inner)))

	for _, h := range m.spec.Header {
		lines = append(lines, FitLines([]string{h}, inner, "")...)
	}
	if len(m.spec.Header) > 0 {
		lines = append(lines, "")
	}

	if len(m.spec.Items) == 0 {
		lines = append(lines, Dim("Nothing here."))
	} else {
		for i, item := range m.spec.Items {
			prefix := "  "
			label := item.Label
			if i == m.cursor {
				prefix = Cyan("▶ ")
				label = Bold(label)
			}
			row := prefix + label
			if item.Description != "" {
				row += "  " + Dim(item.Description)
			}
			lines = append(lines, row)
		}
	}

	lines = append(lines, "")
	if m.status != "" {
		colour := Green
		if !m.statusOK {
			colour = Red
		}
		lines = append(lines, colour(m.status))
	}

	var keys []string
	if m.spec.Select != nil {
		keys = append(keys, Bold("enter")+" select")
	}
	for _, a := range m.spec.Actions {
		keys = append(keys, Bold(a.Key)+" "+a.Label)
	}
	keys = append(keys, Bold("↑↓")+" move", Bold("esc")+" close")
	lines = append(lines, Dim(strings.Join(keys, "   ")))

	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, FitStatus(l, inner))
	}
	if height > 0 && len(out) > height {
		out = out[:height]
	}
	return boxAround(out, width)
}

// boxAround wraps lines in a single-line-drawing border, matching the
// "bordered box because it is modal" framing modal.ts describes. Width is
// the box's outer width, including the border columns.
func boxAround(lines []string, width int) []string {
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	top := "╭" + strings.Repeat("─", inner) + "╮"
	bottom := "╰" + strings.Repeat("─", inner) + "╯"
	out := make([]string, 0, len(lines)+2)
	out = append(out, Gray(top))
	for _, l := range lines {
		out = append(out, Gray("│")+fitWidthPad(l, inner)+Gray("│"))
	}
	out = append(out, Gray(bottom))
	return out
}

// fitWidthPad pads/truncates a line (already fit to `want` visible cells
// by the caller in the common case) to exactly `want` visible columns, so
// the border stays flush.
func fitWidthPad(line string, want int) string {
	w := VisibleWidth(line)
	if w < want {
		return line + strings.Repeat(" ", want-w)
	}
	if w > want {
		return FitStatus(line, want)
	}
	return line
}
