package tui

import (
	"fmt"
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
		"effort  auto · low · medium · high · xhigh · max   ←/→ to adjust",
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

// TestDialogModel_PickMovesCurrentMark: once a switch applies, the ✓ marks
// the model now in use, not the one the dialog opened with
// (qa/findings *model-dialog-stale-state).
func TestDialogModel_PickMovesCurrentMark(t *testing.T) {
	spec := commands.ModalSpec{
		Items:         referenceModelItems(),
		SelectDefault: func(value string) (string, error) { return "Model set to " + value, nil },
	}
	d := NewDialogModel(spec).(*dialogModel)
	d.cursor = 3 // "4. Sonnet"
	_, _, cmd := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	d.Apply(cmd().(msgDialogResult))
	for _, it := range d.spec.Items {
		if marked := it.Marker == "✔"; marked != (it.Value == "d") {
			t.Errorf("item %q marked=%v after picking d", it.Label, marked)
		}
	}
	if spec.Items[1].Marker != "✔" {
		t.Errorf("Apply mutated the caller's items slice")
	}
}

// manyModelItems builds n single-line-description items (no wrap, so each
// is exactly one row) with distinct labels, standing in for a real
// catalog's 17+ models.
func manyModelItems(n int) []commands.Item {
	items := make([]commands.Item, n)
	for i := range items {
		items[i] = commands.Item{
			Value:       string(rune('a' + i)),
			Label:       fmt.Sprintf("model-%02d", i),
			Description: "short",
		}
	}
	return items
}

// TestDialogModel_Render_ShortHeightKeepsHintRowAndWindowsOptions is the
// regression test for qa/findings/20261004T204953Z-narrow-footer-and-
// panel-clipping.json: at 80x24 (Render's height budget after the chrome
// around it is far short of title+17 models+effort+legend), the panel
// used to just render everything and hard-truncate the result to height,
// which always cuts the *bottom* — dropping the legend ("Enter to set as
// default...") and the effort row whatever the cursor was on. Render must
// now keep the legend and effort row always, and instead window the
// options list down to what fits, scrolled to keep the cursor visible.
func TestDialogModel_Render_ShortHeightKeepsHintRowAndWindowsOptions(t *testing.T) {
	spec := commands.ModalSpec{Items: manyModelItems(17), Effort: "medium"}
	d := NewDialogModel(spec).(*dialogModel)
	d.cursor = 0

	const height = 12 // short enough that 17 one-row options don't fit
	got := d.Render(100, height)

	if len(got) > height {
		t.Fatalf("Render returned %d rows, want at most %d", len(got), height)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "Enter to set as default") {
		t.Errorf("legend/hint row missing at height %d:\n%s", height, joined)
	}
	if !strings.Contains(joined, "effort") {
		t.Errorf("effort row missing at height %d:\n%s", height, joined)
	}
	if !strings.Contains(joined, "more below") {
		t.Errorf("expected a \"… +N more below\" row documenting the hidden options:\n%s", joined)
	}

	options, hiddenAbove, hiddenBelow := renderModelOptionRowsWindowed(spec.Items, d.cursor, 100, 4)
	if len(options) == 0 {
		t.Fatalf("windowed options empty")
	}
	if hiddenAbove != 0 {
		t.Errorf("cursor at item 0: hiddenAbove = %d, want 0", hiddenAbove)
	}
	if hiddenBelow == 0 {
		t.Errorf("hiddenBelow = 0, want some items hidden past a 4-row budget over 17 items")
	}
}

// TestDialogModel_Render_CursorStaysVisibleWhenWindowed checks the window
// scrolls with the cursor rather than always showing the first few items:
// with the cursor on the last item, that item's row must still appear.
func TestDialogModel_Render_CursorStaysVisibleWhenWindowed(t *testing.T) {
	spec := commands.ModalSpec{Items: manyModelItems(17)}
	d := NewDialogModel(spec).(*dialogModel)
	d.cursor = 16 // last item

	got := d.Render(100, 12)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "model-16") {
		t.Errorf("cursor's item (model-16, the last one) not shown in windowed render:\n%s", joined)
	}
	if !strings.Contains(joined, "Enter to set as default") {
		t.Errorf("legend/hint row missing with cursor at the end:\n%s", joined)
	}
}

func TestNextEffortLevelReachesAutoAndStopsAtEnds(t *testing.T) {
	steps := []struct {
		from    string
		forward bool
		want    string
	}{
		{"auto", true, "low"},
		{"low", false, "auto"},
		{"auto", false, "auto"},
		{"max", true, "max"},
		{"medium", true, "high"},
	}
	for _, s := range steps {
		if got := nextEffortLevel(s.from, s.forward); got != s.want {
			t.Errorf("nextEffortLevel(%q, forward=%v) = %q, want %q", s.from, s.forward, got, s.want)
		}
	}
	if row := renderEffortScale("high", 30); VisibleWidth(row) > 30 {
		t.Errorf("narrow effort row overflows: %q", row)
	}
}
