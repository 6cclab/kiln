package cli

import (
	"context"
	"encoding/json"
	"os"

	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/mods"
)

// The ui.* services behind $.ui.status/log/notice/notify/copy/selection/
// scroll/focus/blit, and the ui.scroll/ui.focus events the person's keys raise
// on a pane (tui.ModsEngine).

// Has reports whether a mod hooks event.
func (r *modsRuntime) Has(event string) bool {
	e := r.get()
	return e != nil && e.Has(event)
}

// PersonScroll and PersonFocus run the ui.scroll and ui.focus hooks for the
// person's keys on a pane; with no engine the move stands.
func (r *modsRuntime) PersonScroll(ctx context.Context, ev map[string]any) (int, bool) {
	if e := r.get(); e != nil {
		return e.PersonScroll(ctx, ev)
	}
	off, _ := ev["offset"].(int)
	return off, true
}

func (r *modsRuntime) PersonFocus(ctx context.Context, ev map[string]any) (string, bool) {
	if e := r.get(); e != nil {
		return e.PersonFocus(ctx, ev)
	}
	el, _ := ev["element"].(string)
	return el, true
}

// dialogOpen notes that the permission dialog for a tool call is up (or gone):
// $.ui.notice may put a line only under an open one.
func (r *modsRuntime) dialogOpen(id string, open bool) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	if r.dialogs == nil {
		r.dialogs = map[string]bool{}
	}
	if open {
		r.dialogs[id] = true
	} else {
		delete(r.dialogs, id)
	}
	r.mu.Unlock()
	if !open {
		if d := r.driver.Load(); d != nil {
			d.SetNotice(id, "")
		}
	}
}

// setNotifyHooks wires the runner of the person's Notification hooks, which
// $.ui.notify runs before its channel writes.
func (r *modsRuntime) setNotifyHooks(fn func(ctx context.Context, title, text string)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.notifyHooks = fn
	r.mu.Unlock()
}

// notificationHooks returns the runner of the Notification hooks for a
// plugin's notification: notification_type "plugin_notification", message the
// text and title the heading, matched by notification_type as Claude Code does.
func notificationHooks(cfg func() claudehooks.Config, sessionID, transcript, cwd string, onNotice func(string)) func(context.Context, string, string) {
	return func(ctx context.Context, title, text string) {
		claudehooks.RunHooks(claudehooks.RunOptions{
			Config:      cfg(),
			Event:       claudehooks.Notification,
			ToolName:    "plugin_notification",
			HasToolName: true,
			Payload: claudehooks.Payload{
				SessionID:        sessionID,
				TranscriptPath:   transcript,
				Cwd:              cwd,
				Message:          text,
				Title:            title,
				NotificationType: "plugin_notification",
			},
			OnNotice: onNotice,
			Ctx:      ctx,
		})
	}
}

// notifySequence is the escape sequence for a notification on a channel, "" for
// a channel that writes nothing here.
func notifySequence(channel, title, text string) string {
	switch channel {
	case "iterm2":
		return "\x1b]9;\n\n" + title + ":\n" + text + "\x07"
	case "iterm2_with_bell":
		return "\x1b]9;\n\n" + title + ":\n" + text + "\x07\x07"
	case "kitty":
		return "\x1b]99;i=1:d=0;" + title + "\x1b\\\x1b]99;i=1:p=body;" + text + "\x1b\\"
	case "ghostty":
		return "\x1b]777;notify;" + title + ";" + text + "\x07"
	case "terminal_bell":
		return "\x07"
	}
	return ""
}

// notifyChannel is the channel the person's preferredNotifChannel names; "auto"
// (the default) picks by the terminal: iTerm2, Kitty, Ghostty, else the bell.
func notifyChannel(pref string, env func(string) string) string {
	switch pref {
	case "", "auto":
		switch {
		case env("TERM_PROGRAM") == "iTerm.app":
			return "iterm2"
		case env("TERM_PROGRAM") == "ghostty":
			return "ghostty"
		case env("KITTY_WINDOW_ID") != "" || env("TERM") == "xterm-kitty":
			return "kitty"
		}
		return "terminal_bell"
	case "notifications_disabled":
		return ""
	}
	return pref
}

func (r *modsRuntime) uiServices(svc *mods.Services) {
	drv := r.driver.Load
	ui := &svc.UI
	ui.Status = func(plugin, text string) {
		if d := drv(); d != nil {
			d.SetStatus(plugin, text)
		}
	}
	ui.Log = func(_, text string) {
		if d := drv(); d != nil {
			d.Log(text)
		}
	}
	ui.Notice = func(_, id, text string) string {
		r.mu.Lock()
		open := r.dialogs[id]
		r.mu.Unlock()
		d := drv()
		if d == nil || !open {
			return "no dialog is open for " + id
		}
		d.SetNotice(id, text)
		return ""
	}
	ui.Notify = func(ctx context.Context, title, text string) mods.NotifyResult {
		r.mu.Lock()
		hooks := r.notifyHooks
		r.mu.Unlock()
		if hooks != nil {
			hooks(ctx, title, text) // the person's Notification hooks run first
		}
		d := drv()
		if d == nil {
			return mods.NotifyResult{Reason: "no-surface"}
		}
		var pref struct {
			Channel string `json:"preferredNotifChannel"`
		}
		if data, err := os.ReadFile(userClaudeSettingsPath()); err == nil {
			_ = json.Unmarshal(data, &pref)
		}
		channel := notifyChannel(pref.Channel, os.Getenv)
		switch {
		case pref.Channel == "notifications_disabled":
			return mods.NotifyResult{Reason: "disabled"}
		case channel == "" || notifySequence(channel, title, text) == "":
			return mods.NotifyResult{Reason: "no-channel"}
		case !d.Notify(notifySequence(channel, title, text)):
			return mods.NotifyResult{Reason: "refused"}
		}
		return mods.NotifyResult{Channel: channel}
	}
	ui.Copy = func(ctx context.Context, text string) string {
		if d := drv(); d != nil {
			return d.Copy(ctx, text)
		}
		return "no-surface"
	}
	ui.Selection = func() (mods.Selection, bool) {
		if d := drv(); d != nil {
			text, ok := d.Selection()
			return mods.Selection{Text: text}, ok
		}
		return mods.Selection{}, false
	}
	ui.Site = func(plugin, id, key string) (mods.SiteState, bool) {
		if d := drv(); d != nil {
			return d.SiteState(plugin, id, key)
		}
		return mods.SiteState{}, false
	}
	ui.Scroll = func(id string, offset int) {
		if d := drv(); d != nil {
			d.SetScroll(id, offset)
		}
	}
	ui.Focus = func(id, key string) string {
		if d := drv(); d != nil {
			return d.FocusKey(id, key)
		}
		return "kiln is not showing its interface"
	}
	ui.Blit = func(plugin, id, key, cells string) string {
		if d := drv(); d != nil {
			return d.PatchRaster(plugin, id, key, cells)
		}
		return "nothing is mounted: kiln is not showing its interface"
	}
}
