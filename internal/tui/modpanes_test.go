package tui

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/mods"
)

func TestPaneColumns(t *testing.T) {
	req := mods.PaneRequest{Columns: 52}
	cases := []struct {
		name  string
		req   mods.PaneRequest
		width int
		want  int
	}{
		{"wide terminal honours the requested column", req, 160, 52},
		{"at the threshold honours it", req, sidePaneMinWidth, 52},
		{"narrow terminal spans full width", req, 100, 100},
		{"no requested columns spans full width", mods.PaneRequest{}, 160, 160},
		{"requested wider than terminal spans full width", mods.PaneRequest{Columns: 200}, 160, 160},
	}
	for _, c := range cases {
		if got := paneColumns(c.req, c.width); got != c.want {
			t.Errorf("%s: paneColumns = %d, want %d", c.name, got, c.want)
		}
	}
}

// At a wide terminal a pane that asked for 52 columns renders as a 52-wide
// block pushed to the right edge; at a narrow one it spans the full width.
func TestModPanesRender_RightColumnAtWideWidth(t *testing.T) {
	p := &ModPanes{}
	p.Open("squad", mods.PaneRequest{ID: "squad", Title: "Squad", Columns: 52})

	wide := p.Render(160)
	if len(wide) == 0 {
		t.Fatal("no lines rendered")
	}
	// The label rule (the only line for a pane with no drawing yet) fills the
	// full row width but its content is a 52-wide rule pushed right: the left
	// 160-52 columns are padding.
	if w := VisibleWidth(wide[0]); w != 160 {
		t.Errorf("line width = %d, want 160", w)
	}
	if !strings.HasPrefix(wide[0], strings.Repeat(" ", 160-52)) {
		t.Errorf("label rule not right-aligned to a 52-col column: %q", wide[0])
	}

	narrow := p.Render(100)
	if w := VisibleWidth(narrow[0]); w != 100 {
		t.Errorf("narrow line width = %d, want 100", w)
	}
	if strings.HasPrefix(narrow[0], " ") {
		t.Errorf("narrow terminal should render full-width, not right-aligned: %q", narrow[0])
	}
}
