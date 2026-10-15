package tui

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/mods"
)

func box(props map[string]any, children ...mods.Element) mods.Element {
	return mods.Element{Type: "Box", Props: props, Children: children}
}

func text(s string, props map[string]any) mods.Element {
	return mods.Element{Type: "Text", Text: s, Props: props}
}

func TestModRender_BoxRowColumnGapPadding(t *testing.T) {
	withRenderEnv(t, 80)

	tree := box(map[string]any{"flexDirection": "column", "rowGap": float64(1), "padding": float64(1)},
		box(map[string]any{"flexDirection": "row", "columnGap": float64(2)},
			text("left", nil),
			text("right", nil),
		),
		text("second row", nil),
	)

	got := RenderModTree(tree, 30)
	if len(got.Lines) == 0 {
		t.Fatalf("expected lines, got none")
	}
	assertRenderGolden(t, "mod-box-row-column-gap-padding", got.Lines)

	// Row 0 is padding, row 1 is the row box ("left  right"), row 2 is the
	// rowGap blank line, row 3 is "second row", row 4 is bottom padding.
	if !strings.Contains(got.Lines[1], "left") || !strings.Contains(got.Lines[1], "right") {
		t.Fatalf("row box missing children: %q", got.Lines[1])
	}
	if idx := strings.Index(got.Lines[1], "right"); idx-strings.Index(got.Lines[1], "left") != len("left")+2 {
		// "left"(4 cols) + columnGap(2) == the offset from "left" to "right".
		t.Fatalf("columnGap not applied, right starts at wrong column: %q", got.Lines[1])
	}
	for _, l := range got.Lines {
		if VisibleWidth(l) > 30 {
			t.Fatalf("line wider than width 30: %q", l)
		}
	}
}

func TestModRender_TextStylesAndWrapModes(t *testing.T) {
	withRenderEnv(t, 80)

	cases := []struct {
		name string
		el   mods.Element
		want string // substring expected in plain (ANSI-stripped) output
	}{
		{"bold-color", text("hello", map[string]any{"bold": true, "color": "#ff0000"}), "hello"},
		{"truncate-end", text("a long line of text", map[string]any{"wrap": "truncate"}), "…"},
		{"truncate-start", text("a long line of text", map[string]any{"wrap": "truncate-start"}), "…"},
		{"truncate-middle", text("a long line of text", map[string]any{"wrap": "truncate-middle"}), "…"},
		{"wrap", text("a longer line of text that should wrap", map[string]any{"wrap": "wrap"}), "wrap"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RenderModTree(c.el, 10)
			joined := strings.Join(got.Lines, "\n")
			if !strings.Contains(stripANSI(joined), c.want) {
				t.Fatalf("output %q missing %q", joined, c.want)
			}
			for _, l := range got.Lines {
				if VisibleWidth(l) > 10 {
					t.Fatalf("line wider than width 10: %q", l)
				}
			}
		})
	}
}

func TestModRender_ButtonsReportPositionsAndHotkeys(t *testing.T) {
	withRenderEnv(t, 80)

	tree := box(map[string]any{"flexDirection": "row", "columnGap": float64(1)},
		mods.Element{Type: "Button", Props: map[string]any{
			"label":   "Yes",
			"hotkey":  "y",
			"onPress": mods.Callback{ID: 1},
		}},
		mods.Element{Type: "Button", Props: map[string]any{
			"label":   "No",
			"hotkey":  "n",
			"onPress": mods.Callback{ID: 2},
		}},
	)

	got := RenderModTree(tree, 40)
	if len(got.Buttons) != 2 {
		t.Fatalf("expected 2 buttons, got %d: %+v", len(got.Buttons), got.Buttons)
	}
	if got.Buttons[0].Hotkey != "y" || got.Buttons[0].Callback.ID != 1 {
		t.Fatalf("button 0 wrong: %+v", got.Buttons[0])
	}
	if got.Buttons[1].Hotkey != "n" || got.Buttons[1].Callback.ID != 2 {
		t.Fatalf("button 1 wrong: %+v", got.Buttons[1])
	}
	if got.Buttons[0].Row != 0 || got.Buttons[1].Row != 0 {
		t.Fatalf("buttons expected on row 0: %+v", got.Buttons)
	}
	if got.Buttons[1].Col <= got.Buttons[0].Col {
		t.Fatalf("second button should be to the right of the first: %+v", got.Buttons)
	}
	// Button 0 is "[ Yes ]" == 7 columns, plus a 1-column gap.
	if got.Buttons[1].Col != got.Buttons[0].Col+got.Buttons[0].Width+1 {
		t.Fatalf("second button column does not account for first button's width + gap: %+v", got.Buttons)
	}
}

func TestModRender_RasterDecodesCells(t *testing.T) {
	withRenderEnv(t, 80)

	// A 2x1 raster: one orange 'A' on default background, one default-fg
	// 'B' on a blue background.
	cells := make([]byte, 0, 24)
	cells = appendCell(cells, 'A', 0x00ff8800, rasterDefaultColor)
	cells = appendCell(cells, 'B', rasterDefaultColor, 0x000000ff)
	b64 := base64.StdEncoding.EncodeToString(cells)

	tree := mods.Element{Type: "Raster", Props: map[string]any{
		"key":     "sprite",
		"columns": float64(2),
		"rows":    float64(1),
		"cells":   b64,
	}}

	got := RenderModTree(tree, 10)
	if len(got.Lines) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got.Lines))
	}
	plain := stripANSI(got.Lines[0])
	if plain != "AB" {
		t.Fatalf("expected glyphs \"AB\", got %q (raw %q)", plain, got.Lines[0])
	}
	if !strings.Contains(got.Lines[0], "\x1b[") {
		t.Fatalf("expected ANSI styling in raster output: %q", got.Lines[0])
	}
	assertRenderGolden(t, "mod-raster-decodes-cells", got.Lines)
}

func appendCell(cells []byte, codePoint, fg, bg uint32) []byte {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:4], codePoint)
	binary.LittleEndian.PutUint32(buf[4:8], fg)
	binary.LittleEndian.PutUint32(buf[8:12], bg)
	return append(cells, buf...)
}

func TestModRender_NeverWiderThanWidth(t *testing.T) {
	withRenderEnv(t, 80)

	trees := []mods.Element{
		box(map[string]any{"flexDirection": "row", "borderStyle": "round", "padding": float64(2)},
			text("this is a very long piece of text indeed that keeps going", nil),
			mods.Element{Type: "Button", Props: map[string]any{"label": "a very long button label"}},
		),
		box(map[string]any{"flexDirection": "column", "width": "100%", "backgroundColor": "#112233"},
			text("x", map[string]any{"wrap": "truncate"}),
		),
	}
	for _, width := range []int{1, 5, 20, 40, 100} {
		for i, tr := range trees {
			got := RenderModTree(tr, width)
			for _, l := range got.Lines {
				if vw := VisibleWidth(l); vw > width {
					t.Fatalf("tree %d width %d: line %q is %d columns wide", i, width, l, vw)
				}
			}
		}
	}
}

func TestModRender_MalformedPropsDoNotPanic(t *testing.T) {
	withRenderEnv(t, 80)

	trees := []mods.Element{
		{Type: "Box"},
		{Type: "Box", Props: map[string]any{"width": "not-a-number", "padding": "nope", "flexGrow": "nope"}},
		{Type: "Box", Props: map[string]any{"width": float64(1e18), "padding": float64(1e18), "gap": float64(-1e18)}},
		{Type: "Text", Props: map[string]any{"color": 42, "bold": "yes", "wrap": 7}},
		{Type: "Text"},
		{Type: "Button"},
		{Type: "Button", Props: map[string]any{"onPress": "not-a-callback"}},
		{Type: "Raster", Props: map[string]any{"columns": float64(1e18), "rows": float64(1e18), "cells": "!!!not base64!!!"}},
		{Type: "Raster", Props: map[string]any{"columns": float64(4), "rows": float64(4), "cells": "AA=="}},
		{Type: "Image"},
		{Type: "Link"},
		{Type: "Markdown"},
		{Type: "SomethingUnknown", Children: []mods.Element{text("fallback", nil)}},
		{Type: "Box", Props: nil, Children: []mods.Element{{Type: "Box", Props: map[string]any{"flexDirection": 123}}}},
	}

	for i, tr := range trees {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("tree %d panicked: %v", i, r)
				}
			}()
			for _, width := range []int{-5, 0, 1, 500} {
				RenderModTree(tr, width)
			}
		}()
	}
}

func TestModRender_SquadLikeRow(t *testing.T) {
	withRenderEnv(t, 80)

	cells := make([]byte, 0, 4*12)
	for i := 0; i < 4; i++ {
		cells = appendCell(cells, '▲', 0x0061afef, rasterDefaultColor)
	}
	b64 := base64.StdEncoding.EncodeToString(cells)

	row := box(map[string]any{"flexDirection": "row", "columnGap": float64(2)},
		mods.Element{Type: "Raster", Props: map[string]any{"columns": float64(4), "rows": float64(1), "cells": b64}},
		box(map[string]any{"flexDirection": "column", "flexGrow": float64(1)},
			box(map[string]any{"flexDirection": "row", "justifyContent": "space-between"},
				text("coder-1", map[string]any{"bold": true}),
				text("✓", map[string]any{"color": "green"}),
			),
			text("working on the fix", map[string]any{"dimColor": true, "wrap": "truncate-end"}),
		),
		mods.Element{Type: "Button", Props: map[string]any{
			"label":   "open",
			"hotkey":  "o",
			"onPress": mods.Callback{ID: 7},
		}},
	)

	got := RenderModTree(row, 48)
	assertRenderGolden(t, "mod-squad-like-row", got.Lines)

	if len(got.Buttons) != 1 || got.Buttons[0].Hotkey != "o" {
		t.Fatalf("expected the open button with hotkey o: %+v", got.Buttons)
	}
	for _, l := range got.Lines {
		if VisibleWidth(l) > 48 {
			t.Fatalf("line wider than width 48: %q", l)
		}
	}
}

func TestModPanes_OpenCloseFocusKeys(t *testing.T) {
	withRenderEnv(t, 80)

	p := NewModPanes()

	res := p.Open("squad", mods.PaneRequest{ID: "squad-pane", Title: "squad", Focus: true, CloseOnEscape: true, Rows: 6})
	if !res.IsPlaced {
		t.Fatalf("expected the pane to be placed: %+v", res)
	}
	if ids := p.IDs(); len(ids) != 1 || ids[0] != "squad-pane" {
		t.Fatalf("expected one open pane: %v", ids)
	}
	if p.Focused() != "squad-pane" {
		t.Fatalf("expected squad-pane focused, got %q", p.Focused())
	}

	tree := box(map[string]any{"flexDirection": "row", "columnGap": float64(1)},
		mods.Element{Type: "Button", Props: map[string]any{"label": "Yes", "hotkey": "y", "onPress": mods.Callback{ID: 11}}},
		mods.Element{Type: "Button", Props: map[string]any{"label": "No", "hotkey": "n", "onPress": mods.Callback{ID: 12}}},
	)
	p.SetDrawing("squad-pane", mods.Drawing{Plugin: "squad", Tree: &tree})

	lines := p.Render(40)
	if len(lines) == 0 {
		t.Fatalf("expected rendered pane lines")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(stripANSI(joined), "squad") {
		t.Fatalf("expected the pane's title in its label rule: %q", joined)
	}

	// Tab selects the first button, then the hotkey for the second button
	// should still work directly.
	handled, press := p.HandleKey(tea.KeyPressMsg{Code: 'y'})
	if !handled || press == nil || press.ID != 11 {
		t.Fatalf("expected the y hotkey to press button 11: handled=%v press=%+v", handled, press)
	}

	handled, press = p.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if !handled || press != nil {
		t.Fatalf("tab should move selection without pressing: handled=%v press=%+v", handled, press)
	}
	handled, press = p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || press == nil {
		t.Fatalf("enter on the selected button should press it: handled=%v press=%+v", handled, press)
	}

	handled, press = p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !handled || press != nil {
		t.Fatalf("esc should blur/close without a press: handled=%v press=%+v", handled, press)
	}
	if p.Focused() != "" {
		t.Fatalf("expected no pane focused after esc, got %q", p.Focused())
	}
	if ids := p.IDs(); len(ids) != 0 {
		t.Fatalf("expected the pane closed (CloseOnEscape): %v", ids)
	}

	if p.Close("squad-pane") {
		t.Fatalf("expected Close on an already-closed pane to report false")
	}
}
