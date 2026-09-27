//go:build e2e

package e2e

import (
	"strconv"
	"strings"
	"testing"
)

// TestTUI_ContentMargin is the PTY regression test for finding
// no-side-margin: the kiln design pads every block 2 columns from each
// side of the terminal (docs/kiln-design-handoff/Terminal.dc.html's
// transcript/busy-line/palette/input/status padding, 18-20px at 14px Fira
// Code ~2 columns), which internal/tui/layout_margin.go's ContentWidth/
// padMargin/marginFor implement — the terminal body should never start at
// column 0 the way it did before this fix.
//
// Before this fix, marginFor always returned 0 (contentWidth() was m.width
// with no margin subtracted, app.go's own comment on bannerContentWidth
// said as much) — every row here started at column 0 and this test's
// assertions on leading-space counts all failed. Reverting
// internal/tui/layout_margin.go's marginFor to `return 0` unconditionally
// reproduces that failure against this same test.
func TestTUI_ContentMargin(t *testing.T) {
	for _, w := range []int{120, 60} {
		t.Run(colsLabel(w), func(t *testing.T) {
			proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)
			s := startTUI(t, w, 30, proj, home, sessDir, addr,
				"--permission-mode", "bypassPermissions",
			)
			waitReady(t, s)

			s.Send("fix the bug in math.js")
			s.SendKey("enter")
			waitTurnSettled(t, s)

			// Scrollback, not the visible viewport: by the time the turn
			// settles the you-block's echo has long scrolled out of view
			// (inline mode, real scrollback) — the busy line/input
			// box/status row margin is checked separately below, against
			// the still-visible s.Rows().
			rows := s.Scrollback()
			if len(rows) == 0 {
				t.Fatal("no scrollback rendered")
			}

			wantContentWidth := contentWidthForTest(w)

			ruleChecked := false
			for _, r := range rows {
				lead := leadingSpaces(r)
				trimmed := strings.TrimSpace(r)
				if trimmed == "" {
					continue
				}
				// Every non-blank row starts inside the 2-column margin —
				// never at column 0 (finding no-side-margin's "every
				// block starts at column 0").
				if lead < 2 {
					t.Errorf("row has no left margin: %q", r)
				}
				if isRuleRow(r) {
					ruleChecked = true
					if lead != 2 {
						t.Errorf("rule row left margin = %d, want 2: %q", lead, r)
					}
					// The dash run itself is the content width (margin
					// already subtracted from both sides), not the raw
					// terminal width — Terminal.dc.html's rules span the
					// padded body, not the window edge.
					if got := len([]rune(trimmed)); got != wantContentWidth {
						t.Errorf("rule row content width = %d, want %d (raw width %d): %q", got, wantContentWidth, w, r)
					}
				}
			}
			if !ruleChecked {
				t.Error("no rule row found to check the margin against")
			}

			// The you-block's own text (one row below its label rule) also
			// sits inside the margin, not glued to column 0 — finding
			// you-block-no-inner-padding/you-block-surface-width, checked
			// here in the same pass as the outer margin since both add up
			// to the same "text starts a few columns in" observation.
			youIdx := -1
			for i, r := range rows {
				if strings.HasPrefix(strings.TrimLeft(r, " "), "you ") {
					youIdx = i
					break
				}
			}
			if youIdx < 0 || youIdx+1 >= len(rows) {
				t.Fatalf("could not find the you-block's label rule row:\n%s", strings.Join(rows, "\n"))
			}
			if lead := leadingSpaces(rows[youIdx+1]); lead < 3 {
				t.Errorf("you-block text row leading spaces = %d, want at least 3 (2-column margin + 1-column inner pad): %q", lead, rows[youIdx+1])
			}

			// The still-visible busy line/input box/status row (s.Rows())
			// carry the same margin, covering the "live region" half of
			// finding no-side-margin that scrollback checks above don't
			// reach.
			liveRuleChecked := false
			for _, r := range s.Rows() {
				if strings.TrimSpace(r) == "" {
					continue
				}
				if leadingSpaces(r) < 2 {
					t.Errorf("live-region row has no left margin: %q", r)
				}
				if isRuleRow(r) {
					liveRuleChecked = true
				}
			}
			if !liveRuleChecked {
				t.Error("no rule row found in the live region to check the margin against")
			}
		})
	}
}

// TestTUI_ContentMargin_NarrowDropsIt checks the narrow-terminal case this
// port added (layout_margin.go's marginMinWidth, not something the design
// itself specifies): below 40 columns the margin drops to 0 rather than
// crowding already-tight wrapped content further.
func TestTUI_ContentMargin_NarrowDropsIt(t *testing.T) {
	const w = 30
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)
	s := startTUI(t, w, 24, proj, home, sessDir, addr)
	waitReady(t, s)

	rows := s.Rows()
	foundZeroMargin := false
	for _, r := range rows {
		if strings.TrimSpace(r) == "" {
			continue
		}
		if leadingSpaces(r) == 0 {
			foundZeroMargin = true
			break
		}
	}
	if !foundZeroMargin {
		t.Errorf("expected at least one row flush with column 0 below the 40-column margin threshold:\n%s", strings.Join(rows, "\n"))
	}
}

// leadingSpaces counts s's leading ASCII space runes.
func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}

// colsLabel names a subtest by column count, e.g. "120cols".
func colsLabel(w int) string {
	return strconv.Itoa(w) + "cols"
}
