package tui

// The mod pane registry: open/close/focus-by-id, the keys a focused pane
// answers, and drawing each open pane as a label-rule chrome (the
// subagents panel's own pattern, subagents.go) plus its element tree
// (modrender.go) capped to a body height.
import (
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/mods"
)

// defaultPaneBodyRows is the body height a pane gets when its PaneRequest
// does not say (PaneRequest.Rows <= 0).
const defaultPaneBodyRows = 12

// modPane is one open pane's state.
type modPane struct {
	plugin  string
	req     mods.PaneRequest
	drawing mods.Drawing

	// scroll is the first body row shown, clamped into range on the next
	// Render call (it can go out of range between an Up/Down key and the
	// render that follows, which is fine — Render is the single place that
	// clamps it against the tree's actual height).
	scroll int
	// selected indexes lastButtons; the one Tab/Shift+Tab move and Enter
	// presses.
	selected int
	// lastButtons is the full button list from the pane's last Render call
	// (every button in its tree, not only the currently visible rows — Tab
	// cycles through all of them regardless of scroll; a selection that
	// scrolls out of view is a known gap, not a crash).
	lastButtons []ModButton
}

// ModPanes is kiln's mod pane registry: every pane any mod has opened,
// which one (if any) holds the keyboard focus, and the rendering/key state
// each needs. Safe for concurrent use — Open/Close/SetDrawing can arrive
// from the engine's goroutine while Render/HandleKey run on Update's.
type ModPanes struct {
	mu      sync.Mutex
	order   []string
	panes   map[string]*modPane
	focused string
}

// NewModPanes returns an empty registry.
func NewModPanes() *ModPanes {
	return &ModPanes{panes: map[string]*modPane{}}
}

// Open backs $.ui.open. A request whose ID is already open is idempotent
// (returns IsPlaced: true without disturbing the existing pane's scroll or
// drawing).
//
// The 144-column rule for a pane a mod opens without the user having
// asked for it (Claude Code only places such a pane when the terminal is
// at least 144 columns wide) is not implemented: Open has no width to
// check against (ModPanes.Render's caller gives the width, not Open's), so
// every request is placed — the brief's documented fallback ("else always
// place") for exactly this case.
func (p *ModPanes) Open(plugin string, req mods.PaneRequest) mods.PaneResult {
	if p == nil || req.ID == "" {
		return mods.PaneResult{IsPlaced: false, Reason: "missing pane id"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.panes == nil {
		p.panes = map[string]*modPane{}
	}
	if _, exists := p.panes[req.ID]; !exists {
		p.panes[req.ID] = &modPane{plugin: plugin, req: req}
		p.order = append(p.order, req.ID)
	}
	if req.Focus {
		p.focused = req.ID
	}
	return mods.PaneResult{IsPlaced: true}
}

// Close backs $.ui.close/$.ui.panes' removal path. Reports whether a pane
// with this id was open.
func (p *ModPanes) Close(id string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeLocked(id)
}

func (p *ModPanes) closeLocked(id string) bool {
	if _, ok := p.panes[id]; !ok {
		return false
	}
	delete(p.panes, id)
	for i, pid := range p.order {
		if pid == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	if p.focused == id {
		p.focused = ""
	}
	return true
}

// IDs backs $.ui.panes: every open pane id, in open order.
func (p *ModPanes) IDs() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.order))
	copy(out, p.order)
	return out
}

// Focus gives a pane the keyboard focus (a focus chord or a click on it).
// A no-op if id is not open.
func (p *ModPanes) Focus(id string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.panes[id]; ok {
		p.focused = id
	}
}

// Blur drops the focus from whichever pane holds it.
func (p *ModPanes) Blur() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.focused = ""
}

// Focused reports the currently focused pane's id, or "" if none.
func (p *ModPanes) Focused() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.focused
}

// FocusNext moves the focus to the next open pane in open order (wrapping),
// or focuses the first open pane if none currently holds it. Returns the
// newly focused id, or "" if no pane is open. app.go wires this to the
// ctrl+x tab chord; the chord itself is out of this file's scope.
func (p *ModPanes) FocusNext() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.order) == 0 {
		p.focused = ""
		return ""
	}
	if p.focused == "" {
		p.focused = p.order[0]
		return p.focused
	}
	for i, id := range p.order {
		if id == p.focused {
			p.focused = p.order[(i+1)%len(p.order)]
			return p.focused
		}
	}
	// Focused on an id no longer open (should not happen — Close clears
	// it — but tolerate it rather than getting stuck).
	p.focused = p.order[0]
	return p.focused
}

// SetDrawing records what a mod drew for an open pane ($.ui.render's
// result for that pane's Site), replacing whatever it drew last time.
func (p *ModPanes) SetDrawing(id string, d mods.Drawing) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if pane, ok := p.panes[id]; ok {
		pane.drawing = d
	}
}

// Site returns the Site a pane's drawing is rendered for: its body size
// (width minus nothing — a pane has no side chrome of its own beyond the
// label rule above it, which is not part of the body — and its row budget,
// PaneRequest.Rows or defaultPaneBodyRows).
func (p *ModPanes) Site(id string, width int) mods.Site {
	if p == nil {
		return mods.Site{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pane, ok := p.panes[id]
	if !ok {
		return mods.Site{}
	}
	rows := pane.req.Rows
	if rows <= 0 {
		rows = defaultPaneBodyRows
	}
	return mods.Site{
		Component:   "Pane",
		RequestID:   pane.req.ID,
		BodyColumns: max(0, width),
		BodyRows:    rows,
		Surface:     "terminal",
	}
}

// Render draws every open pane: a label rule with the pane's title, then
// its tree rendered to width and capped to its body row budget
// (PaneRequest.Rows, or defaultPaneBodyRows), scrolled to pane.scroll (the
// chrome's own doc comment on clamping it here, not in the key handler).
// A pane with no drawing yet (SetDrawing not called, or its tree is nil —
// the mod called next or has no hook for this site) draws just its label
// rule, no body.
func (p *ModPanes) Render(width int) []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, id := range p.order {
		pane := p.panes[id]
		out = append(out, labelRule(paneTitle(pane), Muted, "", width))
		if pane.drawing.Tree == nil {
			pane.lastButtons = nil
			continue
		}
		rendered := RenderModTree(*pane.drawing.Tree, width)
		pane.lastButtons = rendered.Buttons
		if len(pane.lastButtons) == 0 {
			pane.selected = 0
		} else {
			pane.selected = clamp(pane.selected, 0, len(pane.lastButtons)-1)
		}

		rows := pane.req.Rows
		if rows <= 0 {
			rows = defaultPaneBodyRows
		}
		lines := rendered.Lines
		maxStart := max(0, len(lines)-rows)
		pane.scroll = clamp(pane.scroll, 0, maxStart)
		end := min(len(lines), pane.scroll+rows)
		out = append(out, lines[pane.scroll:end]...)
	}
	return out
}

// paneTitle is a pane's label-rule text: its PaneRequest.Title, or its id
// if the title is empty.
func paneTitle(pane *modPane) string {
	if pane.req.Title != "" {
		return pane.req.Title
	}
	return pane.req.ID
}

// HandleKey answers a key while a pane holds the focus: Tab/Shift+Tab move
// the selected button, a Button's hotkey or Enter on the selected button
// returns its Callback (for the caller to run through the engine's press
// chain), Esc blurs (and closes the pane too, if its PaneRequest asked for
// CloseOnEscape), and Up/Down scroll the body. Returns handled == false
// when no pane is focused or the key means nothing to it, so the caller can
// fall through to its own handling.
//
// The ctrl+x tab chord that moves the focus between panes is app.go's to
// wire (FocusNext); this only handles keys once a pane already has the
// focus.
func (p *ModPanes) HandleKey(msg tea.KeyPressMsg) (handled bool, press *mods.Callback) {
	if p == nil {
		return false, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.focused == "" {
		return false, nil
	}
	pane, ok := p.panes[p.focused]
	if !ok {
		return false, nil
	}

	key := msg.String()
	switch key {
	case "tab":
		if n := len(pane.lastButtons); n > 0 {
			pane.selected = (pane.selected + 1) % n
		}
		return true, nil
	case "shift+tab":
		if n := len(pane.lastButtons); n > 0 {
			pane.selected = (pane.selected - 1 + n) % n
		}
		return true, nil
	case "esc":
		id := p.focused
		p.focused = ""
		if pane.req.CloseOnEscape {
			p.closeLocked(id)
		}
		return true, nil
	case "up":
		pane.scroll = max(0, pane.scroll-1)
		return true, nil
	case "down":
		pane.scroll++
		return true, nil
	case "enter":
		if pane.selected >= 0 && pane.selected < len(pane.lastButtons) {
			cb := pane.lastButtons[pane.selected].Callback
			return true, &cb
		}
		return true, nil
	default:
		for _, b := range pane.lastButtons {
			if b.Hotkey != "" && strings.EqualFold(b.Hotkey, key) {
				cb := b.Callback
				return true, &cb
			}
		}
		return false, nil
	}
}
