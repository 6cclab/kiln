package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// Dialog is a full-screen panel rendered under the transcript in place of
// the input box. This is the interface agreed with the app.go owner: the
// app draws the `▔` rule (with the effort indicator) and composes Render
// below it; keys reach HandleKey first.
//
// Row layout is spelled out in docs/claude-code-reference.md section 5 and
// verified byte-for-byte against testdata/reference/claude-code/
// dialog-model.txt body rows 24-39 (see dialog_test.go
// TestOptionRows_MatchesModelReference): three-space body indent, a
// two-column marker gutter ("  " or "❯ "), the label column padded to the
// widest label plus two spaces, description text starting in that column,
// and any wrapped continuation re-aligned to it.
type Dialog interface {
	// Render returns the dialog's body rows only (no `▔` rule) sized to
	// width x height. Rows beyond height are clipped by the caller's
	// choice of a scrolling list (see renderOptionRows's "↓" marker) rather
	// than by truncating output here.
	Render(width, height int) []string
	// HandleKey applies one keypress. consumed reports whether the dialog
	// used it (a dialog owns the keyboard, so this is almost always true);
	// closeIt reports whether the dialog wants to close.
	HandleKey(msg tea.KeyPressMsg) (consumed, closeIt bool, cmd tea.Cmd)
}

// msgDialogResult carries a Select/Act result back to the open dialog,
// the Dialog-era name for what msgModalResult was.
type msgDialogResult struct {
	msg string
	err error
}

// dialogIndent is the three-space left margin every body row (title,
// description, options, legend) sits at.
const dialogIndent = "   "

// markerWidth is the two-column gutter before a numbered option's label:
// "  " when unselected, "❯ " when selected. Both are exactly two cells.
const markerWidth = 2

// renderTitleAndDescription renders the bold title row followed by the
// dim description wrapped at width-3 (the indent), one blank row after.
func renderTitleAndDescription(title, description string, width int) []string {
	var out []string
	out = append(out, dialogIndent+KilnAmber(Bold(title)))
	if description != "" {
		wrapWidth := width - len(dialogIndent)
		if wrapWidth < 10 {
			wrapWidth = 10
		}
		for _, line := range wrapPlain(description, wrapWidth) {
			out = append(out, dialogIndent+Muted(line))
		}
	}
	return out
}

// wrapPlain word-wraps s at limit visible columns without any ANSI in it
// yet (styling is applied by the caller after wrapping, since colouring
// first would corrupt width accounting).
func wrapPlain(s string, limit int) []string {
	if limit <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if len(cur)+1+len(w) > limit {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	lines = append(lines, cur)
	return lines
}

// DialogOption is one numbered, selectable row in a dialog's list.
type DialogOption struct {
	Label       string // e.g. "2. Opus (1M context)"
	Current     bool   // appends " ✔" after Label, before the description column
	Description string // wrapped and column-aligned under Description
}

// renderOptionRows lays out options starting at row number `startNumber`
// with the numbering already embedded in Label (Label carries "N. " itself
// so callers control numbering, matching ModalSpec.Items order). selected
// is the index of the option the `❯` marker sits on. scrollOffset/visible
// implement the "↓ at the right edge of the last visible row" contract
// when the list is taller than the space given.
func renderOptionRows(options []DialogOption, selected int, width, maxRows int) []string {
	if len(options) == 0 {
		return nil
	}

	// Compute the label column from the widest label (including the " ✔"
	// suffix on the current item, since that suffix sits inside the label
	// field per dialog-model.txt row 29).
	labelWidth := 0
	labels := make([]string, len(options))
	for i, o := range options {
		l := o.Label
		if o.Current {
			l += " ✔"
		}
		labels[i] = l
		if w := VisibleWidth(l); w > labelWidth {
			labelWidth = w
		}
	}
	descCol := len(dialogIndent) + markerWidth + labelWidth + 2

	descWrapWidth := width - descCol
	if descWrapWidth < 10 {
		descWrapWidth = 10
	}

	start, end := 0, len(options)
	scrolled := false
	if maxRows > 0 {
		// Keep the selected row visible; scroll so it never falls
		// outside the window.
		if selected >= maxRows {
			start = selected - maxRows + 1
		}
		end = start + maxRows
		if end > len(options) {
			end = len(options)
			start = end - maxRows
			if start < 0 {
				start = 0
			}
		}
		scrolled = end < len(options) || start > 0
	}

	var out []string
	for i := start; i < end; i++ {
		o := options[i]
		marker := Faint("  ")
		label := Muted(labels[i])
		if i == selected {
			marker = KilnAmber("❯ ")
			label = Ink(labels[i])
		}
		pad := descCol - len(dialogIndent) - markerWidth - VisibleWidth(labels[i])
		if pad < 2 {
			pad = 2
		}
		row := dialogIndent + marker + label
		descLines := wrapPlain(o.Description, descWrapWidth)
		if o.Description != "" {
			row += strings.Repeat(" ", pad) + Muted(descLines[0])
		}
		if i == selected {
			row = OnRaise(padTo(row, width))
		}
		out = append(out, row)
		for _, cont := range descLines[1:] {
			contRow := strings.Repeat(" ", descCol) + Muted(cont)
			if i == selected {
				contRow = OnRaise(padTo(contRow, width))
			}
			out = append(out, contRow)
		}
	}

	if scrolled && len(out) > 0 {
		last := out[len(out)-1]
		lastWidth := VisibleWidth(last)
		gap := width - lastWidth - 1
		if gap < 1 {
			gap = 1
		}
		out[len(out)-1] = last + strings.Repeat(" ", gap) + "↓"
	}

	return out
}

// renderLegend renders the bottom key-hint row, entries joined by " · ",
// dimmed as a whole (matching every reference dialog's legend row).
func renderLegend(entries []string, width int) []string {
	limit := width - len(dialogIndent)
	var rows []string
	cur := ""
	for _, e := range entries {
		switch {
		case cur == "":
			cur = e
		case limit > 0 && len(cur)+len(" · ")+len(e) > limit:
			rows = append(rows, dialogIndent+Muted(cur))
			cur = e
		default:
			cur += " · " + e
		}
	}
	if cur != "" {
		rows = append(rows, dialogIndent+Muted(cur))
	}
	return rows
}

// commandDialog adapts a commands.ModalSpec — the registry-side, TUI
// neutral panel description most built-in commands already return — onto
// the Dialog interface via the generic renderer above. It replaces
// ModalView/NewModalView.
//
// It does not attempt the /model-specific provider grouping or the
// ◐ effort row from the contract: ModalSpec carries a flat Items list with
// no provider or effort concept, and adding those is scoped to whoever
// builds the /model command's ModalSpec (not done here — see the handback
// report).
type commandDialog struct {
	spec     commands.ModalSpec
	cursor   int
	status   string
	statusOK bool
}

// NewCommandDialog builds a Dialog over a commands.ModalSpec, replacing
// NewModalView.
func NewCommandDialog(spec commands.ModalSpec) Dialog {
	switch spec.Kind {
	case "model":
		return NewDialogModel(spec)
	case "mcp":
		return NewDialogMCP(spec)
	}
	return &commandDialog{spec: spec}
}

func (d *commandDialog) HandleKey(msg tea.KeyPressMsg) (consumed, shouldClose bool, cmd tea.Cmd) {
	key := msg.String()
	if key == "esc" {
		return true, true, nil
	}

	switch key {
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}
		return true, false, nil
	case "down", "j":
		if d.cursor < len(d.spec.Items)-1 {
			d.cursor++
		}
		return true, false, nil
	case "enter":
		if d.spec.Select != nil {
			if item, ok := d.selected(); ok {
				sel, value := d.spec.Select, item.Value
				d.status, d.statusOK = "…", true
				return true, false, func() tea.Msg {
					msg, err := sel(value)
					return msgDialogResult{msg: msg, err: err}
				}
			}
		}
		return true, false, nil
	}

	for _, action := range d.spec.Actions {
		if strings.EqualFold(action.Key, key) {
			if d.spec.Act != nil {
				value := ""
				if item, ok := d.selected(); ok {
					value = item.Value
				}
				act, k := d.spec.Act, action.Key
				d.status, d.statusOK = "…", true
				return true, false, func() tea.Msg {
					msg, err := act(k, value)
					return msgDialogResult{msg: msg, err: err}
				}
			}
			return true, false, nil
		}
	}

	return true, false, nil
}

// Apply records a Select/Act result as the panel's status line, the
// Dialog-era name for ModalView.Apply.
func (d *commandDialog) Apply(r msgDialogResult) {
	if r.err != nil {
		d.status, d.statusOK = r.err.Error(), false
		return
	}
	d.status, d.statusOK = r.msg, true
}

func (d *commandDialog) selected() (commands.Item, bool) {
	if d.cursor < 0 || d.cursor >= len(d.spec.Items) {
		return commands.Item{}, false
	}
	return d.spec.Items[d.cursor], true
}

func (d *commandDialog) Render(width, height int) []string {
	var out []string
	out = append(out, renderTitleAndDescription(d.spec.Title, "", width)...)
	for _, h := range d.spec.Header {
		out = append(out, dialogIndent+h)
	}
	if len(d.spec.Header) > 0 {
		out = append(out, "")
	} else {
		out = append(out, "")
	}

	options := make([]DialogOption, len(d.spec.Items))
	for i, item := range d.spec.Items {
		options[i] = DialogOption{
			Label:       item.Label,
			Description: item.Description,
		}
	}
	maxRows := height - len(out) - 3 // room for the blank + status + legend rows
	if maxRows < 1 {
		maxRows = len(options)
	}
	out = append(out, renderOptionRows(options, d.cursor, width, maxRows)...)

	out = append(out, "")
	if d.status != "" {
		colour := KilnGreen
		if !d.statusOK {
			colour = KilnRed
		}
		out = append(out, dialogIndent+colour(d.status))
	}

	var keys []string
	if d.spec.Select != nil {
		keys = append(keys, "Enter to select")
	}
	for _, a := range d.spec.Actions {
		keys = append(keys, a.Key+" "+a.Label)
	}
	keys = append(keys, "Esc to cancel")
	out = append(out, renderLegend(keys, width)...)

	if height > 0 && len(out) > height {
		out = out[:height]
	}
	return out
}
