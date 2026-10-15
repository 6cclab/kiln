package tui

import (
	"context"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/mods"
)

// ModsEngine is what the driver needs from the mod engine
// (*mods.Engine): drawings for a site, and running a pressed button.
type ModsEngine interface {
	Render(ctx context.Context, site mods.Site) []mods.Drawing
	Press(cb mods.Callback)
}

// modsRedrawInterval caps how often mod sites are re-rendered: Claude Code
// redraws a visible pane or band at most 30 times a second.
const modsRedrawInterval = time.Second / 30

// MsgModsRedraw tells the Model the mod drawings changed; View reads them
// from the driver.
type MsgModsRedraw struct{}

// ModsDriver keeps the mods' panes and band drawn. The engine's host calls
// (OpenPane, ClosePane, Panes, Toast, Invalidate) arrive on the engine's
// goroutine; rendering runs on the driver's own goroutine, never on the
// Bubble Tea one, so a slow mod hook cannot stall the TUI.
type ModsDriver struct {
	engine ModsEngine
	panes  *ModPanes

	mu     sync.Mutex
	send   func(tea.Msg)
	width  int
	band   []mods.Drawing
	toasts func(plugin, text string)

	dirty  chan struct{}
	cancel context.CancelFunc
}

// NewModsDriver starts the render loop. send delivers messages to the
// Bubble Tea program (Bridge.SendAsync); nil until the TUI is up, when
// SetSend binds it.
func NewModsDriver(engine ModsEngine) *ModsDriver {
	ctx, cancel := context.WithCancel(context.Background())
	d := &ModsDriver{
		engine: engine,
		panes:  NewModPanes(),
		dirty:  make(chan struct{}, 1),
		cancel: cancel,
	}
	crash.Go(func() { d.loop(ctx) })
	return d
}

// SetSend binds the program's message sink, and where toasts go.
func (d *ModsDriver) SetSend(send func(tea.Msg), toast func(plugin, text string)) {
	d.mu.Lock()
	d.send, d.toasts = send, toast
	d.mu.Unlock()
	d.Invalidate()
}

// Close stops the render loop.
func (d *ModsDriver) Close() { d.cancel() }

// SetWidth records the width sites render at; a change redraws.
func (d *ModsDriver) SetWidth(width int) {
	d.mu.Lock()
	changed := d.width != width
	d.width = width
	d.mu.Unlock()
	if changed {
		d.Invalidate()
	}
}

// Invalidate asks for a redraw; calls coalesce.
func (d *ModsDriver) Invalidate() {
	select {
	case d.dirty <- struct{}{}:
	default:
	}
}

// OpenPane, ClosePane and Panes back $.ui.open/close/panes.
func (d *ModsDriver) OpenPane(plugin string, req mods.PaneRequest) mods.PaneResult {
	res := d.panes.Open(plugin, req)
	d.Invalidate()
	return res
}

func (d *ModsDriver) ClosePane(id string) bool {
	ok := d.panes.Close(id)
	d.Invalidate()
	return ok
}

func (d *ModsDriver) Panes() []string { return d.panes.IDs() }

// Toast shows a mod's short note.
func (d *ModsDriver) Toast(plugin, text string) {
	d.mu.Lock()
	toast := d.toasts
	d.mu.Unlock()
	if toast != nil {
		toast(plugin, text)
	}
}

func (d *ModsDriver) loop(ctx context.Context) {
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.dirty:
		}
		if wait := modsRedrawInterval - time.Since(last); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		last = time.Now()
		d.redraw(ctx)
	}
}

func (d *ModsDriver) redraw(ctx context.Context) {
	d.mu.Lock()
	width, send := d.width, d.send
	d.mu.Unlock()
	if width <= 0 || send == nil {
		return
	}
	for _, id := range d.panes.IDs() {
		site := d.panes.Site(id, width)
		for _, dr := range d.engine.Render(ctx, site) {
			if dr.Tree != nil {
				d.panes.SetDrawing(id, dr)
				break
			}
		}
	}
	band := d.engine.Render(ctx, BandSite(width))
	d.mu.Lock()
	d.band = band
	d.mu.Unlock()
	send(MsgModsRedraw{})
}

// Lines is what the live region shows for mods: the open panes, then the
// band above the prompt.
func (d *ModsDriver) Lines(width int) []string {
	var out []string
	if rows := d.panes.Render(width); len(rows) > 0 {
		out = append(out, "")
		out = append(out, rows...)
	}
	d.mu.Lock()
	band := d.band
	d.mu.Unlock()
	if rows := RenderModBand(band, width); len(rows) > 0 {
		out = append(out, "")
		out = append(out, rows...)
	}
	return out
}

// Focused reports whether a mod pane has the keyboard.
func (d *ModsDriver) Focused() bool { return d.panes.Focused() != "" }

// FocusNext moves the keyboard to the next open pane (Ctrl+X Tab).
func (d *ModsDriver) FocusNext() bool {
	ok := d.panes.FocusNext() != ""
	d.Invalidate()
	return ok
}

// HandleKey routes a key to the focused pane; a pressed button runs off
// the Bubble Tea goroutine.
func (d *ModsDriver) HandleKey(msg tea.KeyPressMsg) bool {
	handled, press := d.panes.HandleKey(msg)
	if press != nil {
		cb := *press
		crash.Go(func() {
			d.engine.Press(cb)
			d.Invalidate()
		})
	}
	if handled {
		d.Invalidate()
	}
	return handled
}
