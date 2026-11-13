package tui

// The ModsDriver's side of $.ui.status/log/notice/notify/copy/selection and of
// the ui.scroll and ui.focus events the person's keys raise on a pane.
import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/mods"
)

// MsgModsLog asks the transcript for a dim system-notice row ($.ui.log).
type MsgModsLog struct{ Text string }

// MsgModsNotify asks the terminal to write a notification's escape sequence.
type MsgModsNotify struct{ Seq string }

// MsgModsCopy asks the terminal to take text onto the clipboard; Reply gets
// the local clipboard tool's error (nil when it took).
type MsgModsCopy struct {
	Text  string
	Reply chan error
}

type modStatus struct{ plugin, text string }

// SetStatus pins a plugin's one status line under the prompt ("" clears it).
func (d *ModsDriver) SetStatus(plugin, text string) {
	d.mu.Lock()
	for i, s := range d.statuses {
		if s.plugin == plugin {
			if text == "" {
				d.statuses = append(d.statuses[:i], d.statuses[i+1:]...)
			} else {
				d.statuses[i].text = text
			}
			plugin = ""
			break
		}
	}
	if plugin != "" && text != "" {
		d.statuses = append(d.statuses, modStatus{plugin, text})
	}
	d.mu.Unlock()
	d.Invalidate()
}

// StatusLines are the plugins' pinned status lines, dim, one row each.
func (d *ModsDriver) StatusLines(width int) []string {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, s := range d.statuses {
		line := strings.ReplaceAll(strings.TrimSpace(s.text), "\n", " ")
		out = append(out, FitStatus("  "+Muted(line), width))
	}
	return out
}

// SetNotice puts a line under the open dialog of a tool call ("" removes it).
func (d *ModsDriver) SetNotice(toolUseID, text string) {
	d.mu.Lock()
	if d.notices == nil {
		d.notices = map[string]string{}
	}
	if text == "" {
		delete(d.notices, toolUseID)
	} else {
		d.notices[toolUseID] = text
	}
	d.mu.Unlock()
	d.Invalidate()
}

// NoticeLines is the line a mod put under the dialog open for toolUseID.
func (d *ModsDriver) NoticeLines(toolUseID string, width int) []string {
	if d == nil || toolUseID == "" {
		return nil
	}
	d.mu.Lock()
	text := d.notices[toolUseID]
	d.mu.Unlock()
	if text == "" {
		return nil
	}
	return []string{FitStatus("  "+Muted(strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")), width)}
}

// SetSelection records what the person last selected with the mouse ("" when
// the highlight went away).
func (d *ModsDriver) SetSelection(text string) {
	d.mu.Lock()
	d.selection = text
	d.mu.Unlock()
}

// Selection is the person's last mouse selection.
func (d *ModsDriver) Selection() (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.selection, d.selection != ""
}

func (d *ModsDriver) sender() func(tea.Msg) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.send
}

// Log appends a dim row to the transcript.
func (d *ModsDriver) Log(text string) {
	if send := d.sender(); send != nil {
		send(MsgModsLog{Text: text})
	}
}

// Notify writes a notification's escape sequence to the terminal.
func (d *ModsDriver) Notify(seq string) bool {
	send := d.sender()
	if send == nil {
		return false
	}
	send(MsgModsNotify{Seq: seq})
	return true
}

// Copy puts text on the clipboard and reports why it did not take ("" when it
// did).
func (d *ModsDriver) Copy(ctx context.Context, text string) string {
	send := d.sender()
	if send == nil {
		return "no-surface"
	}
	reply := make(chan error, 1)
	send(MsgModsCopy{Text: text, Reply: reply})
	select {
	case err := <-reply:
		if err != nil {
			return "no-clipboard"
		}
		return ""
	case <-ctx.Done():
		return "refused"
	case <-time.After(modsReplyWait):
		return "refused"
	}
}

// SiteState, SetScroll, FocusKey and PatchRaster are the pane registry's.
func (d *ModsDriver) SiteState(plugin, requestID, key string) (mods.SiteState, bool) {
	return d.panes.SiteState(plugin, requestID, key)
}

func (d *ModsDriver) SetScroll(id string, offset int) {
	d.panes.SetScroll(id, offset)
	d.Invalidate()
}

func (d *ModsDriver) FocusKey(id, key string) string {
	why := d.panes.FocusKey(id, key)
	d.Invalidate()
	return why
}

func (d *ModsDriver) PatchRaster(plugin, id, key, cells string) string {
	why := d.panes.PatchRaster(plugin, id, key, cells)
	if why == "" {
		d.Invalidate()
	}
	return why
}

// handlePersonMove runs the ui.scroll or ui.focus hooks for the person's Up,
// Down or Tab on a focused pane, off the Bubble Tea goroutine. It reports
// whether it took the key; with no hook on the event the pane moves as before.
func (d *ModsDriver) handlePersonMove(key string) bool {
	switch key {
	case "up", "down":
		if !d.engine.Has("ui.scroll") {
			return false
		}
		id, ev, ok := d.panes.ScrollRequest(key)
		if !ok {
			return false
		}
		crash.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if off, moved := d.engine.PersonScroll(ctx, ev); moved {
				d.panes.SetScroll(id, off)
			}
			d.Invalidate()
		})
		return true
	case "tab", "shift+tab":
		if !d.engine.Has("ui.focus") {
			return false
		}
		id, ev, to, ok := d.panes.FocusRequest(key)
		if !ok {
			return false
		}
		crash.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			el, moved := d.engine.PersonFocus(ctx, ev)
			switch {
			case !moved:
			case el != "":
				d.panes.FocusKey(id, el)
			default:
				d.panes.SetSelected(id, to)
			}
			d.Invalidate()
		})
		return true
	}
	return false
}
