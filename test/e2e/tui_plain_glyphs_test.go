//go:build e2e

package e2e

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// forbiddenPlainGlyphs are the box-drawing/decorative characters plain mode
// (--ax-screen-reader) must never draw — theme.go's Glyphs table has an
// ASCII fallback (ASCIIGlyphs) for exactly these, and every rule call site
// is supposed to route through IsPlain()/G() instead of a hardcoded
// literal (defect *screen-reader-mode-leaves-box-drawing-rules).
var forbiddenPlainGlyphs = []string{"─", "│", "▔", "❯", "⎿"}

// assertNoForbiddenPlainGlyphs fails the test if the current screen
// contains any of forbiddenPlainGlyphs, naming the stage it was checked at.
func assertNoForbiddenPlainGlyphs(t *testing.T, s *screen.Screen, stage string) {
	t.Helper()
	joined := strings.Join(s.Rows(), "\n")
	for _, g := range forbiddenPlainGlyphs {
		if strings.Contains(joined, g) {
			t.Errorf("%s: screen still contains box-drawing glyph %q:\n%s", stage, g, joined)
		}
	}
}

// TestTUI_AxScreenReader_NoBoxDrawingGlyphs drives a full turn under
// --ax-screen-reader through three chrome states the finding named
// explicitly — a running tool call, a permission prompt, and a dialog —
// checking none of forbiddenPlainGlyphs appears at any stage:
//
//   - the input box's own rules (internal/tui/editor/view.go's rule(),
//     unconditionally "─" before this fix's Styles.RuleChar);
//   - the permission prompt's rule (internal/tui/permission_render.go's
//     amberRule, unconditionally "─" before RuleFillChar());
//   - the popup/verbose-notice rule and the dialog frame (app.go's
//     chromeLines/renderPopup, and dialog.go's DialogTopRule → labelRule,
//     already IsPlain-aware and checked here for regressions).
//
// fixBugScript's Read (auto-approved) then Edit (needs a decision under
// manual mode) gives the tool-call + permission-prompt half; opening the
// /model panel afterward gives the dialog half.
func TestTUI_AxScreenReader_NoBoxDrawingGlyphs(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--ax-screen-reader",
		"--permission-mode", "manual",
	)
	if err := s.WaitFor(">", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	assertNoForbiddenPlainGlyphs(t, s, "idle, before the turn")

	s.Send("fix the bug in math.js")
	s.SendKey("enter")

	// manual mode auto-allows the read-only Read and asks only for the
	// Edit that follows it (TestTUI_Permission_Allow's own comment).
	permOrDone := regexp.MustCompile(`Allow kiln to edit|` + turnSummaryPattern.String())
	sawPrompt := false
	for i := 0; i < 5; i++ {
		if err := s.WaitFor(permOrDone, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		if turnSummaryPattern.MatchString(strings.Join(s.Rows(), "\n")) {
			break
		}
		if !sawPrompt {
			// The permission prompt is up right now: this is the one
			// screen this test can catch amberRule's unguarded "─" on.
			assertNoForbiddenPlainGlyphs(t, s, "permission prompt open")
			sawPrompt = true
		}
		s.SendKey("y")
		time.Sleep(50 * time.Millisecond)
	}
	if !sawPrompt {
		t.Fatal("never saw the Edit permission prompt")
	}
	if err := s.WaitFor(turnSummaryPattern, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, s)
	assertNoForbiddenPlainGlyphs(t, s, "after the tool call committed")

	submitSlashCommand(s, "model")
	if err := s.WaitFor("Select model", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	assertNoForbiddenPlainGlyphs(t, s, "dialog open")

	s.SendKey("esc")
	if err := s.WaitFor(">", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	assertNoForbiddenPlainGlyphs(t, s, "after the dialog closed")
}
