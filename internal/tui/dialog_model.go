package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// dialogModel is the Dialog implementation for /model
// (commands.ModalSpec.Kind == "model"): provider-grouped options (grouping
// is by Items order — the spec's own doc comment on Kind says the same;
// the harness's /model command orders its Items by provider already, see
// internal/commands/builtins.go's modelCommand) plus the
// "◐ <effort> effort ←/→ to adjust" row.
//
// Rows verified against testdata/reference/claude-code/dialog-model.txt
// (rows 25-40, terminal width 100) with the harness's own title/
// description substituted per the work item's brief — see
// TestDialogModel_MatchesReferenceStructure in dialog_model_test.go for
// the row-by-row diff.
type dialogModel struct {
	spec     commands.ModalSpec
	cursor   int
	status   string
	statusOK bool
	// picking is the item value an in-flight Select/SelectDefault was
	// run for; Apply moves the current-model mark to it on success.
	picking string
	// effortPending is the level an in-flight SetEffort was run for;
	// Apply shows it on the effort row once it succeeds.
	effortPending string
	// done is set once a pick has applied: the dialog closes, and its
	// Outcome is the confirmation left in the transcript.
	done bool
}

// Done reports that a model was picked and applied.
func (d *dialogModel) Done() bool { return d.done }

// NewDialogModel builds the /model Dialog. Called by
// internal/tui.NewCommandDialog when spec.Kind == "model" — see this
// file's package doc note in the handback report for the exact dispatch
// dialog.go (owned by another agent) needs to add.
func NewDialogModel(spec commands.ModalSpec) Dialog {
	d := &dialogModel{spec: spec}
	for i, it := range spec.Items {
		if it.Marker == "✔" {
			d.cursor = i
		}
	}
	return d
}

const (
	dialogModelTitle       = "Select model"
	dialogModelDescription = "Switch between models. Your pick becomes the default for new sessions. For other names, specify with --model."
)

func (d *dialogModel) HandleKey(msg tea.KeyPressMsg) (consumed, closeIt bool, cmd tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		return true, true, nil
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
	case "left", "right":
		if d.spec.SetEffort == nil {
			// No effort to change (no session lane): arrows do nothing.
			return true, false, nil
		}
		next := nextEffortLevel(d.spec.Effort, key == "right")
		d.status, d.statusOK = "…", true
		d.picking = ""
		d.effortPending = next
		setEffort := d.spec.SetEffort
		return true, false, func() tea.Msg {
			label, err := setEffort(next)
			return msgDialogResult{msg: label, err: err}
		}
	case "s":
		if item, ok := d.selected(); ok && d.spec.Select != nil {
			sel, value := d.spec.Select, item.Value
			d.status, d.statusOK = "…", true
			d.picking = value
			return true, false, func() tea.Msg {
				msg, err := sel(value)
				return msgDialogResult{msg: msg, err: err}
			}
		}
		return true, false, nil
	case "enter":
		if item, ok := d.selected(); ok {
			apply := d.spec.SelectDefault
			if apply == nil {
				apply = d.spec.Select
			}
			if apply != nil {
				value := item.Value
				d.status, d.statusOK = "…", true
				d.picking = value
				return true, false, func() tea.Msg {
					msg, err := apply(value)
					return msgDialogResult{msg: msg, err: err}
				}
			}
		}
		return true, false, nil
	}
	return true, false, nil
}

// Outcome is the transcript row left when the dialog closes: the last
// Select/SelectDefault result, or "Kept model as <current>" when nothing
// was chosen (docs/claude-code-reference.md §3: "❯ /model" / "  ⎿  Kept
// model as Opus 5 (1M context)").
func (d *dialogModel) Outcome() string {
	if d.status != "" && d.status != "…" && d.statusOK {
		return d.status
	}
	for _, it := range d.spec.Items {
		if it.Marker == "✔" {
			label := it.Label
			if i := strings.Index(label, ". "); i > 0 && i < 4 {
				label = label[i+2:]
			}
			return "Kept model as " + label
		}
	}
	return ""
}

// Apply records a Select/SelectDefault/SetEffort result as the dialog's
// status line, mirroring commandDialog.Apply.
func (d *dialogModel) Apply(r msgDialogResult) {
	picked := d.picking
	d.picking = ""
	if r.err != nil {
		d.status, d.statusOK = r.err.Error(), false
		return
	}
	d.status, d.statusOK = r.msg, true
	if d.effortPending != "" {
		d.spec.Effort, d.effortPending = d.effortPending, ""
	}
	if picked != "" {
		d.done = true
		// The ✓ marks the model in use; after a switch that is the pick.
		items := append([]commands.Item(nil), d.spec.Items...)
		for i := range items {
			items[i].Marker = ""
			if items[i].Value == picked {
				items[i].Marker = "✔"
			}
		}
		d.spec.Items = items
	}
}

// effortLevels is the order ←/→ steps through, "auto" (unset: the model
// decides) first. The ends do not wrap: pressing → on max stays on max.
var effortLevels = []string{"auto", "low", "medium", "high", "xhigh", "max"}

func nextEffortLevel(current string, forward bool) string {
	idx := 0 // unrecognised: treat as auto
	for i, l := range effortLevels {
		if strings.EqualFold(l, current) {
			idx = i
			break
		}
	}
	if forward {
		idx = min(idx+1, len(effortLevels)-1)
	} else {
		idx = max(idx-1, 0)
	}
	return effortLevels[idx]
}

// renderEffortScale is the picker's effort row: every level in order,
// the current one amber, then the key hint. A narrow row drops the hint's
// words, then the other levels, before it would overflow.
func renderEffortScale(current string, width int) string {
	parts := make([]string, len(effortLevels))
	for i, l := range effortLevels {
		if strings.EqualFold(l, current) {
			parts[i] = KilnAmber(Bold(l))
		} else {
			parts[i] = Faint(l)
		}
	}
	scale := strings.Join(parts, Faint(" · "))
	for _, row := range []string{
		Muted("effort  ") + scale + Muted("   ←/→ to adjust"),
		Muted("effort  ") + scale + Muted("  ←/→"),
		Muted("effort  ") + KilnAmber(Bold(current)) + Muted("  ←/→"),
	} {
		if VisibleWidth(row) <= width {
			return row
		}
	}
	return FitStatus(Muted("effort  ")+KilnAmber(Bold(current)), width)
}

func (d *dialogModel) selected() (commands.Item, bool) {
	if d.cursor < 0 || d.cursor >= len(d.spec.Items) {
		return commands.Item{}, false
	}
	return d.spec.Items[d.cursor], true
}

// FrameLabel names the label rule DialogTopRule draws above /model.
func (d *dialogModel) FrameLabel() string { return "model" }

// renderModelOptionRows lays out /model's numbered options. It does NOT
// reuse dialog.go's renderOptionRows: that helper wraps descriptions at
// width-descCol, which does not reproduce dialog-model.txt's row 31/32
// wrap (see this file's derivation below and dialog_model_test.go).
//
// Wrap rule derived from testdata/reference/claude-code/dialog-model.txt
// at width 100: descCol (measured from row 29/31, "Opus"/"Fable 5.1"
// starting column) is 31 = len(dialogIndent)+markerWidth+labelWidth+2,
// the SAME formula dialog.go's renderOptionRows already uses (labelWidth
// 24 for "1. Default (recommended)", the widest label at 100 cols) — that
// part was already correct. What was NOT reproduced: wrapping. The
// Fable description ("Fable 5.1 · Most capable for your hardest and
// longest-running tasks", 67 visible columns) wraps in the reference
// after "longest-running" (61 columns) with "tasks" alone on the
// continuation row. width-descCol (100-31=69) is wider than 67, so it
// would NOT wrap under dialog.go's formula. Trying
// descWrapWidth = width - descCol - 3 gives 66, and 61-66 all reproduce
// the exact same greedy-wrap break (verified with a standalone
// simulation of wrapPlain's algorithm against every width in that
// range); 66 is therefore the adopted value: description width = width -
// description column - 3 (a right margin), and it reproduces row 31/32
// exactly at width 100. Not verified at any other width — no second
// reference capture exists to disambiguate 61 from 66 further.
func renderModelOptionRows(items []commands.Item, cursor, width int) []string {
	if len(items) == 0 {
		return nil
	}
	labels := make([]string, len(items))
	labelWidth := 0
	for i, it := range items {
		l := it.Label
		if it.Marker == "✔" {
			l += " " + G().OK
		}
		labels[i] = l
		if w := VisibleWidth(l); w > labelWidth {
			labelWidth = w
		}
	}
	descCol := len(dialogIndent) + markerWidth + labelWidth + 2
	wrapWidth := width - descCol - 3
	if wrapWidth < 10 {
		wrapWidth = 10
	}

	var out []string
	for i, it := range items {
		marker := selectionGutter(i == cursor)
		label := Muted(labels[i])
		if i == cursor {
			label = KilnAmber(labels[i])
		}
		pad := descCol - len(dialogIndent) - markerWidth - VisibleWidth(labels[i])
		if pad < 2 {
			pad = 2
		}
		row := dialogIndent + marker + label
		lines := wrapPlain(it.Description, wrapWidth)
		if it.Description != "" {
			row += strings.Repeat(" ", pad) + Muted(lines[0])
		}
		if i == cursor {
			row = RaiseRow(row, width)
		}
		out = append(out, row)
		for _, cont := range lines[1:] {
			contRow := strings.Repeat(" ", descCol) + Muted(cont)
			if i == cursor {
				contRow = RaiseRow(contRow, width)
			}
			out = append(out, contRow)
		}
	}
	return out
}

func (d *dialogModel) Render(width, height int) []string {
	var out []string
	out = append(out, renderTitleAndDescription(dialogModelTitle, dialogModelDescription, width)...)
	out = append(out, "")
	out = append(out, renderModelOptionRows(d.spec.Items, d.cursor, width)...)
	out = append(out, "")
	if d.spec.Effort != "" {
		out = append(out, dialogIndent+renderEffortScale(d.spec.Effort, width-len(dialogIndent)))
		out = append(out, "")
	}
	if d.status != "" {
		colour := KilnGreen
		if !d.statusOK {
			colour = KilnRed
		}
		out = append(out, dialogIndent+colour(d.status))
		out = append(out, "")
	}
	out = append(out, renderLegend([]string{
		"Enter to set as default",
		"s to use this session only",
		"Esc to cancel",
	}, width)...)

	if height > 0 && len(out) > height {
		out = out[:height]
	}
	return out
}
