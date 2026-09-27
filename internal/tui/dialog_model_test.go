package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// referenceModelItems reproduces the five options from
// testdata/reference/claude-code/dialog-model.txt rows 29-34 verbatim
// (label text and description text, including the Fable row's wrap),
// so TestDialogModel_MatchesReferenceStructure diffs against the exact
// bytes Claude Code produced rather than a paraphrase.
func referenceModelItems() []commands.Item {
	return []commands.Item{
		{Value: "a", Label: "1. Default (recommended)", Description: "Opus 5 with 1M context · Best for everyday, complex tasks"},
		{Value: "b", Label: "2. Opus (1M context)", Marker: "\u2714", Description: "Opus 5 with 1M context · Best for everyday, complex tasks"},
		{Value: "c", Label: "3. Fable", Description: "Fable 5.1 · Most capable for your hardest and longest-running tasks"},
		{Value: "d", Label: "4. Sonnet", Description: "Sonnet 5 · Efficient for routine tasks"},
		{Value: "e", Label: "5. Haiku", Description: "Haiku 4.5 · Fastest for quick answers"},
	}
}

// TestDialogModel_MatchesReferenceStructure diffs the harness's /model
// dialog body against dialog-model.txt rows 25-40 (title through legend),
// width 100, with the harness's own title/description substituted per the
// work item's brief and the kiln restyle's chrome substituted for that
// reference's (QA finding 20260927T000712Z-dialog-chrome-effort-
// indicator): no dialogIndent (dialogIndent is now "" — content aligns
// with every other block's left edge; DialogTopRule's "model ────" label
// rule carries the indent instead), a blank-or-"> " selectionGutter
// instead of "❯", and the current item's checkmark from G().OK ("✓", not
// "✔"). Row 31/32 in the original reference (Fable's wrapped
// description) no longer wraps here: dropping the three-column indent
// widens renderModelOptionRows' wrap column by the same three columns,
// which is enough for "tasks" to fit on Fable's own row at width 100 —
// still consistent with the wrap-width derivation documented on
// renderModelOptionRows, just no longer wrapping at *this* width.
func TestDialogModel_MatchesReferenceStructure(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	spec := commands.ModalSpec{Items: referenceModelItems(), Effort: "Medium"}
	d := NewDialogModel(spec).(*dialogModel)
	d.cursor = 1 // "2. Opus (1M context)" is current, matching the reference's selected row

	got := d.Render(100, 40)

	want := []string{
		"Select model",
		"Switch between models. Your pick becomes the default for new sessions. For other names, specify with",
		"--model.",
		"",
		"  1. Default (recommended)  Opus 5 with 1M context · Best for everyday, complex tasks",
		"> 2. Opus (1M context) \u2713    Opus 5 with 1M context · Best for everyday, complex tasks",
		"  3. Fable                  Fable 5.1 · Most capable for your hardest and longest-running tasks",
		"  4. Sonnet                 Sonnet 5 · Efficient for routine tasks",
		"  5. Haiku                  Haiku 4.5 · Fastest for quick answers",
		"",
		"\u25d0 Medium effort \u2190/\u2192 to adjust",
		"",
		"Enter to set as default \u00b7 s to use this session only \u00b7 Esc to cancel",
	}

	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		row := got[i]
		// The selected row's raised background pads it to the full
		// width (OnRaise(padTo(...))); trim before comparing so this
		// pins content, not background width.
		if i == 5 {
			row = strings.TrimRight(row, " ")
		}
		if row != want[i] {
			t.Errorf("row %d:\n got:  %q\n want: %q", i, row, want[i])
		}
	}
}

// TestDialogModel_Width60 checks the same structural properties (marker,
// numbering, description column, legend) reflow correctly at a narrower
// width; there is no reference capture at 60 columns for /model, so this
// only asserts internal consistency (no row exceeds width, the current
// row's selectionGutter/checkmark survive), not a byte-for-byte diff against Claude Code.
func TestDialogModel_Width60(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	spec := commands.ModalSpec{Items: referenceModelItems(), Effort: "Medium"}
	d := NewDialogModel(spec).(*dialogModel)
	d.cursor = 1

	got := d.Render(60, 40)
	for _, row := range got {
		if VisibleWidth(row) > 60 {
			t.Errorf("row exceeds width 60: %q (%d cols)", row, VisibleWidth(row))
		}
	}

	found := false
	for _, row := range got {
		if strings.Contains(row, "> ") && strings.Contains(row, "2. Opus") && strings.Contains(row, "\u2713") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the current-model row (\"> \" ... \u2713) somewhere in output, got %q", got)
	}
}

// TestDialogModel_EnterCallsSelectDefault checks Enter prefers
// SelectDefault over Select (persistence path), and 's' uses Select
// (session-only), matching the brief's "Enter to set as default · s to
// use this session only" legend.
func TestDialogModel_EnterCallsSelectDefault(t *testing.T) {
	var gotSelect, gotDefault string
	spec := commands.ModalSpec{
		Items: []commands.Item{{Value: "anthropic/opus", Label: "1. Opus"}},
		Select: func(value string) (string, error) {
			gotSelect = value
			return "session ok", nil
		},
		SelectDefault: func(value string) (string, error) {
			gotDefault = value
			return "default ok", nil
		},
	}
	d := NewDialogModel(spec).(*dialogModel)

	if _, _, cmd := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		cmd()
	}
	if gotDefault != "anthropic/opus" || gotSelect != "" {
		t.Errorf("enter: gotDefault=%q gotSelect=%q, want default called, select not", gotDefault, gotSelect)
	}

	gotDefault = ""
	if _, _, cmd := d.HandleKey(tea.KeyPressMsg{Code: 's', Text: "s"}); cmd != nil {
		cmd()
	}
	if gotSelect != "anthropic/opus" || gotDefault != "" {
		t.Errorf("s: gotSelect=%q gotDefault=%q, want select called, default not", gotSelect, gotDefault)
	}
}
