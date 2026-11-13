package tui

// What the ui.* mod calls and events need from the pane registry: a pane's
// scroll window and focus ring as the mods read and move them.
import (
	"github.com/andrepato/harness/internal/mods"
)

// SiteState backs $.ui.scroll and $.ui.focus: the pane plugin owns as
// requestID, or (with "") the one of its panes that draws a Button keyed key.
func (p *ModPanes) SiteState(plugin, requestID, key string) (mods.SiteState, bool) {
	if p == nil {
		return mods.SiteState{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pick := func(id string) (mods.SiteState, bool) {
		pane, ok := p.panes[id]
		if !ok || pane.plugin != plugin {
			return mods.SiteState{}, false
		}
		s := mods.SiteState{Component: "Pane", RequestID: id, Offset: pane.scroll, BodyRows: pane.bodyRows,
			Rows: pane.contentRows, KeyRow: -1, Focused: p.focused == id}
		for _, b := range pane.lastButtons {
			if key != "" && b.Key == key {
				s.HasKey, s.TakesFocus, s.KeyRow = true, true, b.Row
				break
			}
		}
		return s, true
	}
	if requestID != "" {
		return pick(requestID)
	}
	for _, id := range p.order {
		if s, ok := pick(id); ok && s.HasKey {
			return s, true
		}
	}
	return mods.SiteState{}, false
}

// SetScroll moves a pane's window; Render clamps it into range.
func (p *ModPanes) SetScroll(id string, offset int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pane, ok := p.panes[id]; ok {
		pane.scroll = max(0, offset)
	}
}

// FocusKey puts a pane's focus ring on the Button keyed key; it returns why it
// could not.
func (p *ModPanes) FocusKey(id, key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	pane, ok := p.panes[id]
	if !ok {
		return "no pane is open as " + id
	}
	for i, b := range pane.lastButtons {
		if b.Key == key {
			pane.selected = i
			return ""
		}
	}
	return "no Button keyed " + key + " is drawn in " + id
}

// PatchRaster swaps the cells of the Raster keyed key that plugin drew in pane
// id, for the next frame; it returns why it could not.
func (p *ModPanes) PatchRaster(plugin, id, key, cells string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	pane, ok := p.panes[id]
	if !ok || pane.drawing.Tree == nil || pane.drawing.Plugin != plugin {
		return "no pane of " + plugin + " is mounted as " + id
	}
	var walk func(el *mods.Element) bool
	walk = func(el *mods.Element) bool {
		if el.Type == "Raster" && el.Key == key {
			props := make(map[string]any, len(el.Props)+1)
			for k, v := range el.Props {
				props[k] = v
			}
			props["cells"] = cells
			el.Props = props
			return true
		}
		for i := range el.Children {
			if walk(&el.Children[i]) {
				return true
			}
		}
		return false
	}
	if !walk(pane.drawing.Tree) {
		return "no Raster keyed " + key + " is drawn in " + id
	}
	return ""
}

// ScrollRequest is the ui.scroll event for an Up or Down key on the focused
// pane (what the window would move to, before any hook has its say).
func (p *ModPanes) ScrollRequest(key string) (id string, ev map[string]any, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pane, found := p.panes[p.focused]
	if !found || (key != "up" && key != "down") {
		return "", nil, false
	}
	by := 1
	if key == "up" {
		by = -1
	}
	last := max(0, pane.contentRows-pane.bodyRows)
	offset := clamp(pane.scroll+by, 0, last)
	return p.focused, map[string]any{"component": "Pane", "requestId": p.focused, "offset": offset,
		"by": by, "bodyRows": pane.bodyRows, "contentRows": pane.contentRows}, true
}

// FocusRequest is the ui.focus event for a Tab or Shift+Tab on the focused
// pane, and the index of the Button the ring would land on.
func (p *ModPanes) FocusRequest(key string) (id string, ev map[string]any, to int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pane, found := p.panes[p.focused]
	n := len(pane.lastButtons)
	if !found || n == 0 || (key != "tab" && key != "shift+tab") {
		return "", nil, 0, false
	}
	to = (pane.selected + 1) % n
	if key == "shift+tab" {
		to = (pane.selected - 1 + n) % n
	}
	ev = map[string]any{"component": "Pane", "requestId": p.focused, "plugin": pane.plugin}
	if k := pane.lastButtons[to].Key; k != "" {
		ev["element"] = k
	}
	return p.focused, ev, to, true
}

// SetSelected puts the focus ring on the pane's idx-th Button.
func (p *ModPanes) SetSelected(id string, idx int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pane, ok := p.panes[id]; ok && idx >= 0 && idx < len(pane.lastButtons) {
		pane.selected = idx
	}
}
