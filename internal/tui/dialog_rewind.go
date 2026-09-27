package tui

import (
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// RewindEntry is one user message the Rewind dialog can rewind to.
type RewindEntry struct {
	ID           string
	Text         string
	FilesChanged int
}

// RewindEntriesFromSession picks the user messages out of a lane's
// entries, oldest first, for the Rewind dialog's option list.
// FilesChanged is always 0 for now: entries carry no per-message file-diff
// count today (Entry has no such field — see internal/session/types.go),
// so every row renders "No code changes" until that data exists somewhere
// to read.
func RewindEntriesFromSession(entries []session.Entry) []RewindEntry {
	// entries may be newest-first (Lane.FindEntries's contract) or
	// oldest-first depending on the caller; sort defensively by Seq so the
	// dialog always lists oldest first regardless of what it was handed.
	sorted := make([]session.Entry, len(entries))
	copy(sorted, entries)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1].Seq > sorted[j].Seq; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}

	var out []RewindEntry
	for _, e := range sorted {
		if e.Type != session.EntryMessage {
			continue
		}
		um, ok := e.Message.(msg.UserMessage)
		if !ok {
			continue
		}
		text := firstLineOf(um)
		if text == "" {
			continue
		}
		out = append(out, RewindEntry{ID: e.ID, Text: text, FilesChanged: 0})
	}
	return out
}

// firstLineOf joins a user message's text blocks and returns their first
// line, matching what the reference dialog shows per option ("run this
// shell command and report its output: echo parity-check" in
// dialog-rewind.txt row 4, the literal first line of that turn's prompt).
func firstLineOf(um msg.UserMessage) string {
	var text string
	for _, c := range um.Content {
		if tc, ok := c.(msg.TextContent); ok {
			text += tc.Text
		}
	}
	for i, r := range text {
		if r == '\n' {
			return text[:i]
		}
	}
	return text
}

// rewindDialog is Esc-Esc-on-an-empty-input: pick a point in the
// conversation (or "(current)", the no-op) to rewind to. Row layout was
// originally verified byte-for-byte against
// testdata/reference/claude-code/dialog-rewind.txt rows 2-7 in
// dialog_rewind_test.go (row 1, that reference's `▔` rule with the
// effort indicator, was drawn by the app, not by Render here — DialogTopRule
// replaces it with the kiln label rule, "rewind ────", per FrameLabel below).
type rewindDialog struct {
	entries  []RewindEntry
	onSelect func(entryID string) error
	cursor   int // index into entries; len(entries) means "(current)"
}

// NewRewindDialog builds the Rewind Dialog. entries must be oldest first
// (RewindEntriesFromSession already orders them that way). onSelect is
// called when a message row is chosen with that entry's ID; it is not
// called for "(current)", which is a pure no-op close.
//
// Wiring this into the harness is out of scope here: app.go's Esc-Esc
// handler on an empty input should build entries via
// RewindEntriesFromSession(lane.FindEntries(ctx)) and pass
// lane.NavigateTree(ctx, &entryID) (or NavigateTree(ctx, nil) for "back to
// root") as onSelect — see internal/harness/lane.go's NavigateTree
// (Lane.NavigateTree(ctx context.Context, targetID *string) error): it
// moves the lane's branch tip to targetID (nil for the root), refusing
// while an operation is running. That call is not made by this file; no
// other file was touched to wire it.
func NewRewindDialog(entries []RewindEntry, onSelect func(entryID string) error) Dialog {
	return &rewindDialog{entries: entries, onSelect: onSelect, cursor: len(entries)}
}

// FrameLabel names the label rule DialogTopRule draws above Rewind.
func (d *rewindDialog) FrameLabel() string { return "rewind" }

func (d *rewindDialog) Render(width, height int) []string {
	var out []string
	out = append(out, dialogIndent+KilnAmber(Bold("Rewind")))
	out = append(out, dialogIndent+Muted("Restore the code and/or conversation to the point before…"))

	labelWidth := width - len(dialogIndent) - markerWidth
	if labelWidth < 10 {
		labelWidth = 10
	}
	subIndent := dialogIndent + "  " // marker gutter's two cells, so the sub-row re-aligns under the label

	for i, e := range d.entries {
		marker := selectionGutter(i == d.cursor)
		label := Muted(FitStatus(e.Text, labelWidth))
		selected := i == d.cursor
		if selected {
			label = KilnAmber(FitStatus(e.Text, labelWidth))
		}
		row := dialogIndent + marker + label
		if selected {
			row = OnRaise(padTo(row, width))
		}
		out = append(out, row)

		sub := "No code changes"
		if e.FilesChanged > 0 {
			sub = pluralFilesChanged(e.FilesChanged)
		}
		subRow := subIndent + Muted(sub)
		if selected {
			subRow = OnRaise(padTo(subRow, width))
		}
		out = append(out, subRow)
	}

	currentLabel := "(current)"
	currentSelected := d.cursor == len(d.entries)
	currentMarker := selectionGutter(currentSelected)
	currentText := Muted(currentLabel)
	if currentSelected {
		currentText = KilnAmber(currentLabel)
	}
	currentRow := dialogIndent + currentMarker + currentText
	if currentSelected {
		currentRow = OnRaise(padTo(currentRow, width))
	}
	out = append(out, currentRow)

	out = append(out, renderLegend([]string{"Enter to continue", "Esc to cancel"}, width)...)

	if height > 0 && len(out) > height {
		out = out[:height]
	}
	return out
}

func pluralFilesChanged(n int) string {
	if n == 1 {
		return "1 file changed"
	}
	return strconv.Itoa(n) + " files changed"
}

func (d *rewindDialog) HandleKey(msg tea.KeyPressMsg) (consumed, closeIt bool, cmd tea.Cmd) {
	last := len(d.entries)
	switch msg.String() {
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}
		return true, false, nil
	case "down", "j":
		if d.cursor < last {
			d.cursor++
		}
		return true, false, nil
	case "enter":
		if d.cursor == last {
			return true, true, nil
		}
		entry := d.entries[d.cursor]
		var runErr error
		if d.onSelect != nil {
			runErr = d.onSelect(entry.ID)
		}
		return true, true, func() tea.Msg {
			return msgDialogResult{msg: "Rewound to before: " + entry.Text, err: runErr, replay: runErr == nil}
		}
	case "esc":
		return true, true, nil
	}
	return true, false, nil
}
