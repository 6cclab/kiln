package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/commands"
)

// TestOptionRows_MatchesModelReference checks the generic option-row
// layout (indent, marker gutter, label column from the widest label,
// current-item "✔", description column) against the exact body rows
// Claude Code produced in testdata/reference/claude-code/dialog-model.txt
// (rows 28, 29, 32, 33 — the ones that do not wrap), captured at terminal
// width 100. Colour is disabled for the comparison since the reference
// file is plain text; column math is what's under test here, not styling.
//
// Row 30/31 (the "Fable" option, which wraps its description onto a
// second line in the reference) is deliberately NOT asserted here: the
// reference wraps that description well short of width-3 (limit is
// somewhere between 61 and 66 columns, not 97), and the exact formula
// Claude Code uses for the option-description wrap width could not be
// determined with confidence from the one capture available — see the
// handback report. wrapPlain here wraps at width-descCol, which is wider
// than the reference and therefore NOT verified to match; treat option
// description wrapping as unverified ([chk]) until a second reference
// capture at a different width pins the formula down.
func TestOptionRows_MatchesModelReference(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	options := []DialogOption{
		{Label: "1. Default (recommended)", Description: "Opus 5 with 1M context · Best for everyday, complex tasks"},
		{Label: "2. Opus (1M context)", Current: true, Description: "Opus 5 with 1M context · Best for everyday, complex tasks"},
		{Label: "4. Sonnet", Description: "Sonnet 5 · Efficient for routine tasks"},
		{Label: "5. Haiku", Description: "Haiku 4.5 · Fastest for quick answers"},
	}

	got := renderOptionRows(options, 1, 100, 0)

	want := []string{
		"     1. Default (recommended)  Opus 5 with 1M context · Best for everyday, complex tasks",
		"   ❯ 2. Opus (1M context) ✔    Opus 5 with 1M context · Best for everyday, complex tasks",
		"     4. Sonnet                 Sonnet 5 · Efficient for routine tasks",
		"     5. Haiku                  Haiku 4.5 · Fastest for quick answers",
	}

	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		row := got[i]
		// The selected row's raised background pads it to the full
		// width; trim before comparing so this pins content, not
		// background width.
		if i == 1 {
			row = strings.TrimRight(row, " ")
		}
		if row != want[i] {
			t.Errorf("row %d:\n got:  %q\n want: %q", i, row, want[i])
		}
	}
}

func TestRenderOptionRows_ScrollIndicator(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	var options []DialogOption
	for i := 0; i < 10; i++ {
		options = append(options, DialogOption{Label: "N. item", Description: "d"})
	}
	got := renderOptionRows(options, 0, 40, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 visible rows, got %d", len(got))
	}
	last := got[len(got)-1]
	if !strings.HasSuffix(last, "↓") {
		t.Errorf("expected scroll indicator on last visible row, got %q", last)
	}
}

func TestRenderTitleAndDescription_WrapsAtIndent(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	got := renderTitleAndDescription("Select model", "one two three four five six seven eight nine ten", 20)
	if got[0] != "   Select model" {
		t.Errorf("title row = %q", got[0])
	}
	for _, l := range got[1:] {
		if VisibleWidth(l) > 20 {
			t.Errorf("wrapped row exceeds width: %q", l)
		}
	}
}

func TestRenderLegend_JoinsWithMiddleDot(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	got := renderLegend([]string{"Enter to select", "Esc to cancel"}, 100)[0]
	want := "   Enter to select · Esc to cancel"
	if got != want {
		t.Errorf("legend = %q, want %q", got, want)
	}
}

func TestCommandDialog_RenderAt100And60(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	spec := commands.ModalSpec{
		Title: "Test dialog",
		Items: []commands.Item{
			{Value: "a", Label: "a", Description: "first item"},
			{Value: "b", Label: "b", Description: "second item"},
		},
		Select: func(value string) (string, error) { return "ok: " + value, nil },
	}

	for _, width := range []int{100, 60} {
		d := NewCommandDialog(spec)
		rows := d.Render(width, 20)
		if len(rows) == 0 {
			t.Fatalf("width %d: no rows rendered", width)
		}
		for _, r := range rows {
			if VisibleWidth(r) > width {
				t.Errorf("width %d: row exceeds width: %q", width, r)
			}
		}
	}
}

func TestCommandDialog_EscCloses(t *testing.T) {
	d := NewCommandDialog(commands.ModalSpec{Title: "t"})
	_, closeIt, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !closeIt {
		t.Error("esc should request close")
	}
}
