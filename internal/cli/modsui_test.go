package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/mods"
)

func TestNotifyChannelFollowsPreferenceThenTerminal(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		pref string
		env  map[string]string
		want string
	}{
		{"", map[string]string{"TERM_PROGRAM": "iTerm.app"}, "iterm2"},
		{"auto", map[string]string{"TERM_PROGRAM": "ghostty"}, "ghostty"},
		{"auto", map[string]string{"TERM": "xterm-kitty"}, "kitty"},
		{"auto", map[string]string{}, "terminal_bell"},
		{"iterm2_with_bell", map[string]string{"TERM_PROGRAM": "ghostty"}, "iterm2_with_bell"},
		{"notifications_disabled", map[string]string{}, ""},
	}
	for _, c := range cases {
		if got := notifyChannel(c.pref, env(c.env)); got != c.want {
			t.Errorf("notifyChannel(%q, %v) = %q, want %q", c.pref, c.env, got, c.want)
		}
	}
	if got := notifySequence("ghostty", "CI", "done"); got != "\x1b]777;notify;CI;done\x07" {
		t.Errorf("ghostty sequence = %q", got)
	}
	if !strings.Contains(notifySequence("iterm2", "CI", "done"), "CI:\ndone") || notifySequence("nope", "a", "b") != "" {
		t.Error("iterm2 or unknown-channel sequence is wrong")
	}
}

func TestUIServicesWithoutAnInterfaceAnswerNoSurface(t *testing.T) {
	r := &modsRuntime{}
	var s mods.Services
	r.uiServices(&s)
	if got := s.UI.Notify(context.Background(), "t", "x"); got.Reason != "no-surface" {
		t.Errorf("notify = %+v", got)
	}
	if got := s.UI.Copy(context.Background(), "x"); got != "no-surface" {
		t.Errorf("copy = %q", got)
	}
	if _, ok := s.UI.Selection(); ok {
		t.Error("a selection with no interface")
	}
	if why := s.UI.Notice("p", "t1", "x"); why == "" {
		t.Error("a notice with no dialog open was accepted")
	}
	if _, ok := s.UI.Site("p", "files", ""); ok {
		t.Error("a site with no interface")
	}
	if s.UI.Focus("files", "k") == "" || s.UI.Blit("p", "files", "k", "AA==") == "" {
		t.Error("focus or blit succeeded with no interface")
	}
	s.UI.Status("p", "x")
	s.UI.Log("p", "x")
	s.UI.Scroll("files", 3)
}

func TestDialogTrackingGatesNotices(t *testing.T) {
	r := &modsRuntime{}
	r.dialogOpen("t1", true)
	r.mu.Lock()
	open := r.dialogs["t1"]
	r.mu.Unlock()
	if !open {
		t.Fatal("the dialog was not noted as open")
	}
	r.dialogOpen("t1", false)
	r.mu.Lock()
	open = r.dialogs["t1"]
	r.mu.Unlock()
	if open {
		t.Error("the dialog stayed open after it closed")
	}
	var nilRT *modsRuntime
	nilRT.dialogOpen("t1", true) // no mods: nothing to note
}

func TestPersonMovesWithoutEngineStand(t *testing.T) {
	r := &modsRuntime{}
	if off, ok := r.PersonScroll(context.Background(), map[string]any{"offset": 4}); !ok || off != 4 {
		t.Errorf("scroll = %d %v", off, ok)
	}
	if el, ok := r.PersonFocus(context.Background(), map[string]any{"element": "k"}); !ok || el != "k" {
		t.Errorf("focus = %q %v", el, ok)
	}
	if r.Has("ui.scroll") {
		t.Error("Has with no engine")
	}
}
