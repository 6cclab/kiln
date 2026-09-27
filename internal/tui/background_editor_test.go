package tui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestBackgroundColorMsg_RebuildsEditorStyles is the internal/tui half of
// defect *light-bg-you-text-invisible: NewModel builds the editor's Styles
// once, from theme.go's design-default tokens, before the terminal's real
// background is known (SetTerminalBackground only runs later, on the first
// tea.BackgroundColorMsg — see app.go's Init doc comment). Without pushing
// the recomputed tokens back into the already-built editor, its rule
// colours stayed the dark-design ones forever, confirmed against
// qa/runs/c3/iterm-light/content-verbose-toggle-120x40/02-reply-non-
// verbose.png: the input box's own rule sampled at RGB(50,44,36) — the
// unadjusted design hexRuleStrong — while every rule committed after
// background detection read the adapted RGB(212,210,203) instead.
//
// This drives the actual production path (Update's tea.BackgroundColorMsg
// case, not SetTerminalBackground or editorStyles in isolation) and checks
// the editor's own rendered rule line changed, and changed to exactly what
// editorStyles' current tokens would produce — pinning the mechanism, not
// just "something is different".
func TestBackgroundColorMsg_RebuildsEditorStyles(t *testing.T) {
	prevEnabled := enabled
	t.Cleanup(func() {
		SetColorEnabled(prevEnabled)
		resetSurfaceTokensToDesign()
		resetTextTokensToDesign()
	})
	SetColorEnabled(true)
	resetSurfaceTokensToDesign()
	resetTextTokensToDesign()

	m := newTestModel()
	before := m.editor.View(40)[0]

	next, _ := m.Update(tea.BackgroundColorMsg{Color: color.RGBA{R: 0xf7, G: 0xf4, B: 0xee, A: 0xff}})
	m2, ok := next.(Model)
	if !ok {
		t.Fatalf("Update(tea.BackgroundColorMsg) returned %T, want Model", next)
	}

	after := m2.editor.View(40)[0]
	if after == before {
		t.Fatalf("editor's top rule unchanged after a background-colour reply: still %q", after)
	}

	want := editorStyles(G().UserMark).Rule.Render(strings.Repeat(RuleFillChar(), 40))
	if after != want {
		t.Errorf("editor's top rule after SetTerminalBackground = %q, want %q (the theme package's *current* RuleStrong token, not a stale design one)", after, want)
	}
}
