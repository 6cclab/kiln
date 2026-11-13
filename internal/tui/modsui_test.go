package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/mods"
)

// hookedEngine hooks ui.scroll and ui.focus and records what reaches them.
type hookedEngine struct {
	fakePromptEngine
	mu       sync.Mutex
	scrolls  []map[string]any
	focuses  []map[string]any
	scrollTo int
	focusTo  string
	keep     bool
	done     chan struct{}
}

func (h *hookedEngine) Has(ev string) bool { return ev == "ui.scroll" || ev == "ui.focus" }
func (h *hookedEngine) PersonScroll(_ context.Context, ev map[string]any) (int, bool) {
	h.mu.Lock()
	h.scrolls = append(h.scrolls, ev)
	h.mu.Unlock()
	defer func() { h.done <- struct{}{} }()
	return h.scrollTo, !h.keep
}
func (h *hookedEngine) PersonFocus(_ context.Context, ev map[string]any) (string, bool) {
	h.mu.Lock()
	h.focuses = append(h.focuses, ev)
	h.mu.Unlock()
	defer func() { h.done <- struct{}{} }()
	return h.focusTo, !h.keep
}

// threeButtonPane opens a focused pane of plugin "p" drawing 30 rows with three
// keyed Buttons, and renders it so its rows and buttons are known.
func threeButtonPane(t *testing.T) *ModPanes {
	t.Helper()
	p := NewModPanes()
	p.Open("p", mods.PaneRequest{ID: "files", Rows: 5, Focus: true})
	var kids []mods.Element
	for i := 0; i < 30; i++ {
		switch i {
		case 3, 12, 25:
			kids = append(kids, mods.Element{Type: "Button", Key: "row:" + string(rune('a'+i)), Props: map[string]any{"label": "b"}})
		default:
			kids = append(kids, mods.Element{Type: "Text", Text: "x"})
		}
	}
	root := mods.Element{Type: "Box", Props: map[string]any{"flexDirection": "column"}, Children: kids}
	p.SetDrawing("files", mods.Drawing{Plugin: "p", Tree: &root})
	p.Render(40)
	return p
}

func TestModPanesSiteStateReportsWindowAndKeys(t *testing.T) {
	p := threeButtonPane(t)
	s, ok := p.SiteState("p", "files", "row:m")
	if !ok || s.Component != "Pane" || s.BodyRows != 5 || s.Rows != 30 || !s.Focused || !s.HasKey || s.KeyRow != 12 {
		t.Errorf("SiteState = %+v %v, want a 5-row window over 30 rows, key row 12", s, ok)
	}
	if s, ok := p.SiteState("p", "", "row:z"); !ok || s.RequestID != "files" || s.KeyRow != 25 {
		t.Errorf("by key alone = %+v %v, want the pane that draws it", s, ok)
	}
	if _, ok := p.SiteState("other", "files", ""); ok {
		t.Error("another plugin's pane was reported")
	}
}

func TestModPanesScrollAndFocusMovesAreClamped(t *testing.T) {
	p := threeButtonPane(t)
	p.SetScroll("files", 99)
	out := p.Render(40)
	if !strings.Contains(strings.Join(out, "\n"), "b") || len(out) != 1+5 {
		t.Errorf("render after a far scroll = %d rows, want the rule and 5 body rows", len(out))
	}
	if s, _ := p.SiteState("p", "files", ""); s.Offset != 25 {
		t.Errorf("offset = %d, want clamped to 25 (30 rows less a 5-row window)", s.Offset)
	}
	if why := p.FocusKey("files", "row:z"); why != "" {
		t.Errorf("FocusKey = %q", why)
	}
	if _, ev, to, ok := p.FocusRequest("tab"); !ok || to != 0 || ev["element"] != "row:d" {
		t.Errorf("tab from the last button = %v to %d, want a wrap to the first (row:d)", ev, to)
	}
	if why := p.FocusKey("files", "nope"); why == "" {
		t.Error("FocusKey on an undrawn key succeeded")
	}
}

func TestModPanesPatchRasterOnlyForItsOwner(t *testing.T) {
	p := NewModPanes()
	p.Open("p", mods.PaneRequest{ID: "r"})
	root := mods.Element{Type: "Box", Children: []mods.Element{{Type: "Raster", Key: "view", Props: map[string]any{"cells": "AA==", "columns": float64(1)}}}}
	p.SetDrawing("r", mods.Drawing{Plugin: "p", Tree: &root})
	if why := p.PatchRaster("q", "r", "view", "AQ=="); why == "" {
		t.Error("another plugin repainted a Raster it did not draw")
	}
	if why := p.PatchRaster("p", "r", "nope", "AQ=="); why == "" {
		t.Error("a key that is not drawn was repainted")
	}
	if why := p.PatchRaster("p", "r", "view", "AQ=="); why != "" {
		t.Fatalf("PatchRaster = %q", why)
	}
	if got := root.Children[0].Props["cells"]; got != "AQ==" {
		t.Errorf("cells = %v, want the blit", got)
	}
}

func newHookedDriver(t *testing.T, eng *hookedEngine) *ModsDriver {
	t.Helper()
	eng.done = make(chan struct{}, 4)
	d := NewModsDriver(eng)
	t.Cleanup(d.Close)
	d.SetSend(func(tea.Msg) {}, nil)
	d.panes = threeButtonPane(t)
	return d
}

func waitDone(t *testing.T, eng *hookedEngine) {
	t.Helper()
	select {
	case <-eng.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the ui hook never ran")
	}
	time.Sleep(20 * time.Millisecond)
}

func TestPersonScrollGoesThroughUIScrollHooks(t *testing.T) {
	eng := &hookedEngine{scrollTo: 7}
	d := newHookedDriver(t, eng)
	if !d.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) {
		t.Fatal("a hooked scroll key was not taken")
	}
	waitDone(t, eng)
	if len(eng.scrolls) != 1 || eng.scrolls[0]["by"] != 1 || eng.scrolls[0]["offset"] != 1 || eng.scrolls[0]["contentRows"] != 30 || eng.scrolls[0]["bodyRows"] != 5 {
		t.Errorf("ui.scroll event = %v, want by 1 to offset 1 over 30 rows in a 5-row window", eng.scrolls)
	}
	if s, _ := d.SiteState("p", "files", ""); s.Offset != 7 {
		t.Errorf("offset = %d, want the hook's 7", s.Offset)
	}
}

func TestPersonScrollAHookKeptLeavesWindowAlone(t *testing.T) {
	eng := &hookedEngine{scrollTo: 7, keep: true}
	d := newHookedDriver(t, eng)
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	waitDone(t, eng)
	if s, _ := d.SiteState("p", "files", ""); s.Offset != 0 {
		t.Errorf("offset = %d, want 0: the hook answered without next", s.Offset)
	}
}

func TestPersonFocusGoesThroughUIFocusHooks(t *testing.T) {
	eng := &hookedEngine{focusTo: "row:z"}
	d := newHookedDriver(t, eng)
	if !d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}) {
		t.Fatal("a hooked focus key was not taken")
	}
	waitDone(t, eng)
	if len(eng.focuses) != 1 || eng.focuses[0]["element"] != "row:m" || eng.focuses[0]["plugin"] != "p" {
		t.Errorf("ui.focus event = %v, want the next button (row:m) of plugin p", eng.focuses)
	}
	if why := d.panes.FocusKey("files", "row:z"); why != "" {
		t.Fatal(why)
	}
	d.panes.mu.Lock()
	sel := d.panes.panes["files"].selected
	d.panes.mu.Unlock()
	if sel != 2 {
		t.Errorf("focus ring on button %d, want the hook's row:z (2)", sel)
	}
}

func TestUnhookedKeysMoveThePaneAtOnce(t *testing.T) {
	d := NewModsDriver(newFakePromptEngine())
	t.Cleanup(d.Close)
	d.panes = threeButtonPane(t)
	if !d.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) {
		t.Fatal("down was not handled")
	}
	d.panes.Render(40)
	if s, _ := d.SiteState("p", "files", ""); s.Offset != 1 {
		t.Errorf("offset = %d, want 1 with no ui.scroll hook", s.Offset)
	}
}

// rowEngine draws every site as one Text, and hooks ui.render.
type rowEngine struct {
	fakePromptEngine
	site mods.Site
	none bool
}

func (r *rowEngine) Has(ev string) bool { return ev == "ui.render" }
func (r *rowEngine) Render(_ context.Context, s mods.Site) []mods.Drawing {
	r.site = s
	if r.none {
		return []mods.Drawing{{Plugin: "p"}}
	}
	return []mods.Drawing{{Plugin: "p", Tree: &mods.Element{Type: "Text", Text: "row for " + s.RequestID}}}
}

func TestDrawRowRendersTheFirstTreeAtWidth(t *testing.T) {
	eng := &rowEngine{}
	d := NewModsDriver(eng)
	t.Cleanup(d.Close)
	rows := d.DrawRow("CommandOutput", "status", map[string]any{"text": "x"}, 40)
	if len(rows) != 1 || !strings.Contains(rows[0], "row for status") {
		t.Errorf("rows = %q", rows)
	}
	if eng.site.Component != "CommandOutput" || eng.site.Props["text"] != "x" || eng.site.BodyColumns != 40 {
		t.Errorf("site = %+v", eng.site)
	}
	eng.none = true
	if rows := d.DrawRow("CommandOutput", "status", nil, 40); rows != nil {
		t.Errorf("a hook that drew nothing gave rows %q", rows)
	}
	var nilDriver *ModsDriver
	if nilDriver.DrawRow("CommandOutput", "s", nil, 40) != nil {
		t.Error("a nil driver drew")
	}
	if NewModsDriverForTest(t).DrawRow("CommandOutput", "s", nil, 40) != nil {
		t.Error("a driver with no ui.render hook drew")
	}
}

// NewModsDriverForTest is a driver over an engine with no hooks.
func NewModsDriverForTest(t *testing.T) *ModsDriver {
	t.Helper()
	d := NewModsDriver(newFakePromptEngine())
	t.Cleanup(d.Close)
	return d
}

func TestStatusAndNoticeLines(t *testing.T) {
	d := NewModsDriver(newFakePromptEngine())
	t.Cleanup(d.Close)
	if got := d.StatusLines(40); len(got) != 0 {
		t.Fatalf("status lines before any = %v", got)
	}
	d.SetStatus("a", "building")
	d.SetStatus("b", "two\nlines")
	d.SetStatus("a", "built")
	got := strings.Join(d.StatusLines(40), "|")
	if !strings.Contains(got, "built") || strings.Contains(got, "building") || !strings.Contains(got, "two lines") {
		t.Errorf("status lines = %q, want one line per plugin, the newer text replacing the old", got)
	}
	d.SetStatus("a", "")
	if n := len(d.StatusLines(40)); n != 1 {
		t.Errorf("after a clear, %d lines, want 1", n)
	}
	d.SetNotice("t1", "checked by p")
	if got := d.NoticeLines("t1", 40); len(got) != 1 || !strings.Contains(got[0], "checked by p") {
		t.Errorf("notice = %v", got)
	}
	if got := d.NoticeLines("t2", 40); len(got) != 0 {
		t.Errorf("notice for another call = %v", got)
	}
	d.SetNotice("t1", "")
	if got := d.NoticeLines("t1", 40); len(got) != 0 {
		t.Errorf("notice after removal = %v", got)
	}
	var nilDriver *ModsDriver
	if nilDriver.StatusLines(40) != nil || nilDriver.NoticeLines("x", 40) != nil {
		t.Error("a nil driver drew lines")
	}
}

func TestModsLogCommitsASystemNoticeRow(t *testing.T) {
	m, _, _ := modsPromptModel(t)
	if _, cmd := m.Update(MsgModsLog{Text: "from a mod"}); cmd != nil {
		t.Errorf("a log row returned a command %v", cmd)
	}
}

func TestModsCopyAndNotifyReturnCommands(t *testing.T) {
	t.Setenv("HARNESS_CLIPBOARD_CMD", "cat >/dev/null")
	m, _, d := modsPromptModel(t)
	reply := make(chan error, 1)
	_, cmd := m.Update(MsgModsCopy{Text: "hello", Reply: reply})
	if cmd == nil {
		t.Fatal("no copy command")
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			go c()
		}
	}
	select {
	case err := <-reply:
		if err != nil {
			t.Errorf("copy reported %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the copy never reported")
	}
	if _, cmd := m.Update(MsgModsNotify{Seq: "\a"}); cmd == nil {
		t.Error("no notify command")
	}
	d.SetSelection("picked")
	if s, ok := d.Selection(); !ok || s != "picked" {
		t.Errorf("selection = %q %v", s, ok)
	}
}
