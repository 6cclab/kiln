//go:build e2e

package e2e

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// This file drives kiln's opt-in full-screen (alt-screen) TUI mode
// (docs/kiln-fullscreen-plan.md, --fullscreen / ctrl+f) through the same
// PTY-attached driver tui_test.go uses for inline mode. It follows that
// file's conventions exactly: startTUI, waitReady, waitTurnSettled,
// waitQuiescent, tuiFixture, fixBugScript, assertGolden/goldens under
// testdata/golden (UPDATE=1 regenerates them).

// fixBugThenSecondScript extends fixBugScript with a second, tool-free
// turn ("Second turn done.") so a test can drive two separate user
// submissions against one faux server — fixBugScript alone is exhausted
// after the fix-bug turn's own multi-step tool round trip (see
// internal/testkit/faux/script.go's flattenSteps: each plain default step
// with no on_tool_result gate becomes its own turn, consumed one per
// request).
const fixBugThenSecondScript = fixBugScript + `  - text: "Second turn done."
    usage: {input: 12, output: 5}
`

// --- 1. Startup, no turn -------------------------------------------------

func TestTUI_Fullscreen_Startup(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--fullscreen")
	waitReady(t, s)

	rows := s.Viewport()
	if len(rows) != 30 {
		t.Fatalf("Viewport() returned %d rows, want 30", len(rows))
	}

	// The bottom row is the mode line (the alt-screen frame fills the
	// whole terminal; there is no scrollback to grow into).
	last := rows[29]
	if !modeLinePattern.MatchString(last) {
		t.Errorf("last row is not the mode line: %q", last)
	}

	// The editor marker sits above the bottom rule, i.e. somewhere in the
	// bottom region but not on the very last row.
	editorRow := -1
	for i, r := range rows {
		if strings.Contains(r, tuiUserMark) {
			editorRow = i
			break
		}
	}
	if editorRow < 0 {
		t.Fatalf("editor marker %q not found on screen:\n%s", tuiUserMark, strings.Join(rows, "\n"))
	}
	if editorRow >= 29 {
		t.Errorf("editor marker on row %d, want it above the bottom mode-line row (29)", editorRow)
	}

	// The banner's tip row ("/ commands …") is near the top of the
	// transcript viewport.
	tipRow := -1
	for i, r := range rows {
		if strings.Contains(r, "/ commands") {
			tipRow = i
			break
		}
	}
	if tipRow < 0 {
		t.Fatalf("banner tip row (\"/ commands\") not found on screen:\n%s", strings.Join(rows, "\n"))
	}
	if tipRow > 8 {
		t.Errorf("banner tip row at %d, want it near the top of the screen", tipRow)
	}

	// Alt-screen has no scrollback.
	if sb := s.Scrollback(); len(sb) != 0 {
		t.Errorf("Scrollback() = %d lines, want 0 in fullscreen mode:\n%s", len(sb), strings.Join(sb, "\n"))
	}

	assertGoldenNormalizedBanner(t, s, "tui-fullscreen-empty-100x30")
}

// --- 2. Fix-bug flow ------------------------------------------------------

func TestTUI_Fullscreen_FixBug(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--fullscreen",
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("fix the bug in math.js")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	joined := strings.Join(s.Rows(), "\n")
	// Read-only calls commit their own full "tool" block now instead of
	// collapsing to "Read N files" (app.go's flushGroup, kiln UI pass
	// Phase 2.1); the diff block has no separate "Update <path>" head
	// line any more (Phase 2.2) — see TestTUI_FixBug's identical
	// assertion in tui_test.go for the full rationale.
	if !regexp.MustCompile(`read ─+[^\n]*\n\s*src/math\.js`).MatchString(joined) {
		t.Errorf("transcript missing the full \"read\" tool block:\n%s", joined)
	}
	if !strings.Contains(joined, "src/math.js") || !strings.Contains(joined, "+1") || !strings.Contains(joined, "−1") {
		t.Errorf("transcript missing the diff header row (path + counts):\n%s", joined)
	}
	// The turn-summary row is gone (kiln design: the busy line just
	// disappears at turn end, nothing is committed in its place), so
	// "the turn produced its final text" is now checked directly.
	if !strings.Contains(joined, "Fixed.") {
		t.Errorf("transcript missing the assistant's final turn text:\n%s", joined)
	}

	rows := s.Rows()
	last := rows[len(rows)-1]
	if !modeLinePattern.MatchString(last) {
		t.Errorf("last row is not the mode line: %q", last)
	}

	if sb := s.Scrollback(); len(sb) != 0 {
		t.Errorf("Scrollback() = %d lines, want 0 in fullscreen mode:\n%s", len(sb), strings.Join(sb, "\n"))
	}

	assertGoldenNormalizedBanner(t, s, "tui-fullscreen-fix-bug")

	fixed, err := os.ReadFile(proj + "/src/math.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fixed), "return a + b;") {
		t.Errorf("src/math.js not fixed on disk:\n%s", fixed)
	}
}

// --- 3. Scroll-to-pause ----------------------------------------------------

// TestTUI_Fullscreen_ScrollPause drives a terminal small enough (100x14)
// that the fix-bug transcript exceeds the viewport, so it auto-follows to
// the bottom while busy/idle, and checks PgUp pauses that auto-follow: a
// second turn's output must not yank the view back to the bottom while the
// user is scrolled up reading history (docs/kiln-fullscreen-plan.md's
// "scroll-to-pause").
func TestTUI_Fullscreen_ScrollPause(t *testing.T) {
	proj, home, sessDir, addr, requests := tuiFixture(t, fixBugThenSecondScript)

	s := startTUI(t, 100, 14, proj, home, sessDir, addr,
		"--fullscreen",
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("fix the bug in math.js")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	// At the bottom (auto-followed): the turn's final text is visible, the
	// banner (scrolled off the top of the small viewport) is not. (The
	// turn-summary row this used to check is gone — the busy line just
	// disappears at turn end, nothing committed in its place.)
	joined := strings.Join(s.Rows(), "\n")
	if !strings.Contains(joined, "Fixed.") {
		t.Fatalf("assistant's final turn text not visible while auto-followed at the bottom:\n%s", joined)
	}
	if strings.Contains(joined, "/ commands") {
		t.Fatalf("banner tip row unexpectedly visible in a %d-row terminal after a turn that should have pushed it off-screen:\n%s", 14, joined)
	}

	// PgUp: scroll toward the top. The banner (or at least an earlier row)
	// becomes visible, the turn summary scrolls off, and the mode line
	// stays pinned to the last row (the bottom region does not move). At
	// 14 rows the transcript is much taller than one page, so this presses
	// PgUp repeatedly rather than assuming one press reaches the top.
	bannerVisible := false
	for i := 0; i < 10 && !bannerVisible; i++ {
		s.SendKey("pgup")
		time.Sleep(30 * time.Millisecond)
		if strings.Contains(strings.Join(s.Rows(), "\n"), "/ commands") {
			bannerVisible = true
		}
	}
	if !bannerVisible {
		t.Fatalf("pgup never scrolled the banner into view:\n%s", strings.Join(s.Rows(), "\n"))
	}
	if err := waitQuiescent(s, 100*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	rows := s.Rows()
	joined = strings.Join(rows, "\n")
	if strings.Contains(joined, "Fixed.") {
		t.Errorf("first turn's final text still visible after pgup scrolled to the banner:\n%s", joined)
	}
	if !modeLinePattern.MatchString(rows[len(rows)-1]) {
		t.Errorf("last row is not the mode line after pgup: %q", rows[len(rows)-1])
	}

	// Capture the exact top-of-screen row while scrolled up, before a new
	// turn's output commits.
	topBefore := s.Viewport()[0]

	// Submit a second prompt while scrolled up. The faux script's second
	// turn ("Second turn done.") answers it without any tool round trip.
	before := len(requests())
	s.Send("go again")
	s.SendKey("enter")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(requests()) <= before {
		time.Sleep(20 * time.Millisecond)
	}
	if len(requests()) <= before {
		t.Fatalf("faux server never received the second turn's request (before=%d, after=%d)", before, len(requests()))
	}
	if err := waitQuiescent(s, 250*time.Millisecond, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	// The scrolled view did not jump: the exact same row is still on top.
	topAfter := s.Viewport()[0]
	if topAfter != topBefore {
		t.Errorf("scrolled view jumped when the second turn's output committed:\nbefore: %q\nafter:  %q", topBefore, topAfter)
	}
	if strings.Contains(strings.Join(s.Rows(), "\n"), "Second turn done.") {
		t.Errorf("second turn's reply is visible while still scrolled up — scroll-to-pause did not hold:\n%s", strings.Join(s.Rows(), "\n"))
	}

	// PgDn back to the bottom: the new turn's reply becomes visible.
	found := false
	for i := 0; i < 15 && !found; i++ {
		s.SendKey("pgdown")
		time.Sleep(30 * time.Millisecond)
		if strings.Contains(strings.Join(s.Rows(), "\n"), "Second turn done.") {
			found = true
		}
	}
	if !found {
		t.Errorf("second turn's reply (\"Second turn done.\") never became visible after pgdown:\n%s", strings.Join(s.Rows(), "\n"))
	}
}

// --- 4. Resize re-wrap -----------------------------------------------------

func TestTUI_Fullscreen_Resize(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 120, 30, proj, home, sessDir, addr,
		"--fullscreen",
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("fix the bug in math.js")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	assertResizeRewrapsFullscreen := func(w, h int) {
		t.Helper()
		s.Resize(w, h)
		// The rewrap is debounced 150ms after the last WindowSizeMsg
		// (app.go's msgFullscreenRewrap); wait past that before checking.
		time.Sleep(350 * time.Millisecond)
		if err := waitQuiescent(s, 200*time.Millisecond, 3*time.Second); err != nil {
			t.Fatalf("resize to %dx%d: %v", w, h, err)
		}

		// Rows() itself asserts the per-row width invariant.
		rows := s.Rows()
		joined := strings.Join(rows, "\n")
		if !strings.Contains(joined, "src/math.js") {
			t.Errorf("resize to %dx%d: transcript lost the diff block's path after re-wrap:\n%s", w, h, joined)
		}
		if !modeLinePattern.MatchString(rows[len(rows)-1]) {
			t.Errorf("resize to %dx%d: last row is not the mode line: %q", w, h, rows[len(rows)-1])
		}

		// A rule row (all '─') spans exactly the new content width, proving
		// the content actually re-wrapped rather than just being truncated
		// at the old width. Content width, not the raw terminal width: a
		// rule now sits inside the 2-column side margin
		// (internal/tui/layout_margin.go, finding no-side-margin) like
		// every other full-width block, so its dash run is
		// margin.ContentWidth(w) long, not w.
		ruleWidth := -1
		for _, r := range s.Viewport() {
			if isRuleRow(r) {
				ruleWidth = len([]rune(strings.TrimSpace(r)))
				break
			}
		}
		wantWidth := contentWidthForTest(w)
		if ruleWidth != wantWidth {
			t.Errorf("resize to %dx%d: rule row width = %d, want %d", w, h, ruleWidth, wantWidth)
		}
	}

	assertResizeRewrapsFullscreen(60, 24)
	assertResizeRewrapsFullscreen(120, 30)
}

// --- 5. /model dialog in the bottom region ---------------------------------

func TestTUI_Fullscreen_ModelDialog(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--fullscreen")
	waitReady(t, s)

	submitSlashCommand(s, "model")
	if err := s.WaitFor("Select model", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitQuiescent(s, 100*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	rows := s.Viewport()
	if len(rows) != 30 {
		t.Fatalf("Viewport() returned %d rows, want 30", len(rows))
	}
	titleRow := -1
	for i, r := range rows {
		if strings.Contains(r, "Select model") {
			titleRow = i
			break
		}
	}
	if titleRow < 0 {
		t.Fatalf("dialog title row not found:\n%s", strings.Join(rows, "\n"))
	}
	// The dialog replaces the bottom region (input box + mode line), not
	// the transcript above it — it must be in the lower half of the
	// screen, and its last row must still be within the terminal.
	if titleRow < 15 {
		t.Errorf("dialog title on row %d, want it in the bottom region (>= 15) below the transcript/banner", titleRow)
	}

	s.SendKey("esc")
	if err := s.WaitFor(tuiUserMark, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitQuiescent(s, 100*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	rows2 := s.Rows()
	if strings.Contains(strings.Join(rows2, "\n"), "Select model") {
		t.Error("dialog title still present after esc closed it")
	}
	editorRow := -1
	for i, r := range rows2 {
		if strings.Contains(r, tuiUserMark) {
			editorRow = i
			break
		}
	}
	if editorRow < 0 {
		t.Fatalf("editor marker not found after closing the dialog:\n%s", strings.Join(rows2, "\n"))
	}
	// The input box returns pinned near the bottom of the screen, not back
	// up near the banner.
	if editorRow < len(rows2)-6 {
		t.Errorf("editor marker on row %d of %d, want it pinned near the bottom", editorRow, len(rows2))
	}
	last := rows2[len(rows2)-1]
	if !modeLinePattern.MatchString(last) {
		t.Errorf("last row is not the mode line after closing the dialog: %q", last)
	}
}

// --- 6. ctrl+f toggle -------------------------------------------------------

func TestTUI_Fullscreen_ToggleKey(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	// Start WITHOUT --fullscreen: the default inline layout.
	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)

	inlineHeight := s.OccupiedHeight()
	if inlineHeight >= 30 {
		t.Fatalf("inline start already occupies the full screen (OccupiedHeight=%d) — the fixture can't distinguish inline from fullscreen", inlineHeight)
	}
	// Inline mode has already committed the startup banner to real
	// scrollback via tea.Println (unlike a session started with
	// --fullscreen from the outset, entering fullscreen later does not
	// retroactively erase what inline already printed — a real terminal's
	// alt screen leaves existing primary-buffer scrollback alone). Record
	// it now so the assertion below can check it does not grow further
	// once the bridge's commit sink switches to the transcript buffer.
	sbBefore := len(s.Scrollback())

	// ctrl+f: enter fullscreen.
	s.SendKey("ctrl+f")
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows := s.Rows()
		if len(rows) == 30 && modeLinePattern.MatchString(rows[len(rows)-1]) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ctrl+f never produced a full 30-row frame with the mode line pinned to the bottom; last screen:\n%s", strings.Join(s.Rows(), "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	rows := s.Rows()
	if !strings.Contains(strings.Join(rows, "\n"), "/ commands") {
		t.Errorf("banner not visible at the top after entering fullscreen:\n%s", strings.Join(rows, "\n"))
	}
	editorRow := -1
	for i, r := range rows {
		if strings.Contains(r, tuiUserMark) {
			editorRow = i
			break
		}
	}
	if editorRow < 0 {
		t.Fatalf("editor marker not found after entering fullscreen:\n%s", strings.Join(rows, "\n"))
	}
	if editorRow < len(rows)-6 {
		t.Errorf("editor marker on row %d of %d, want it pinned near the bottom in fullscreen", editorRow, len(rows))
	}
	if sb := len(s.Scrollback()); sb != sbBefore {
		t.Errorf("Scrollback() grew from %d to %d lines while in fullscreen — new commits should land in the transcript buffer, not native scrollback", sbBefore, sb)
	}

	// ctrl+f again: leave fullscreen, back to inline.
	s.SendKey("ctrl+f")
	if err := s.WaitFor(tuiUserMark, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	rows = s.Rows()
	last := rows[len(rows)-1]
	if !modeLinePattern.MatchString(last) {
		t.Errorf("last non-empty row is not the mode line after leaving fullscreen: %q", last)
	}
	// Leaving the alt screen restores the primary buffer's content from
	// before ctrl+f was first pressed (a real terminal's alt-screen
	// contract) rather than staying pinned full-height like fullscreen
	// was: OccupiedHeight drops back down, distinguishing this frame from
	// the fullscreen one above.
	postHeight := s.OccupiedHeight()
	if postHeight >= 30 {
		t.Errorf("OccupiedHeight()=%d after leaving fullscreen, want < 30 (back to the inline, non-bottom-pinned layout)", postHeight)
	}
}

// --- 7. --ax-screen-reader falls back to inline -----------------------------

func TestTUI_Fullscreen_AxScreenReaderFallsBackInline(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 80, 24, proj, home, sessDir, addr, "--fullscreen", "--ax-screen-reader")
	if err := s.WaitFor(">", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	assertAXFooterInvariant(t, s)

	// --fullscreen is a no-op under --ax-screen-reader (Plain mode has no
	// alt-screen rendering to fall back from — see app.go's NewModel and
	// toggleFullscreen's doc comment): the screen must match the plain
	// empty-box golden byte for byte, not a new fullscreen-shaped one.
	want, err := os.ReadFile(goldenPath("tui-ax-empty-80.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(normalizeBannerCwdRow(s.Rows()), "\n") + "\n"
	if got != string(want) {
		t.Errorf("--fullscreen --ax-screen-reader does not match the plain-mode empty golden tui-ax-empty-80.txt\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}

	// Not pinned to the bottom: the box sits near the top, the same as
	// plain inline mode (small OccupiedHeight, not a full 24-row frame).
	if h := s.OccupiedHeight(); h >= 24 {
		t.Errorf("OccupiedHeight()=%d, want < 24 — --fullscreen appears pinned to the bottom under --ax-screen-reader", h)
	}
}
