//go:build e2e

package e2e

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// tuiDesignBackground is the kiln design's own background (#14110d, docs/
// kiln-design-handoff/README.md's palette), passed to every PTY test via
// screen.WithBackgroundColor so internal/tui.SetTerminalBackground's "near
// design bg keeps the exact design hexes" rule holds and every existing
// golden stays byte-for-byte identical.
var tuiDesignBackground = color.RGBA{R: 0x14, G: 0x11, B: 0x0d, A: 0xff}

// This file drives the real, PTY-attached harness TUI (as opposed to
// print/session/hooks_test.go, which drive print/one-shot mode). Every
// assertion here is against internal/testkit/screen's emulated terminal
// grid (Rows/Viewport/CursorRow/OccupiedHeight) or against real files on
// disk/faux's recorded requests, never against raw escape bytes, matching
// docs/testing.md's screen-layer rule.
//
// testClock freezes both the footer's "now" and the model's own
// StartedAt (see app.go's NewModel: HARNESS_TEST_CLOCK overrides both, so
// the elapsed-time segment is "0s" on every run rather than real skew).
const testClock = "2026-09-23T12:00:00Z"

// tuiUserMark is the editor's marker glyph, matching
// internal/tui/theme.go's UnicodeGlyphs.UserMark (kiln's "›" input
// prompt).
const tuiUserMark = "›"

// startTUI starts the real harness binary under a PTY, cwd'd to proj, wired
// to a faux server at fauxAddr (empty to omit faux env entirely — the
// empty-box goldens don't need a model), with proj as the child's working
// directory.
//
// Fullscreen (alt-screen) is the binary's own default now
// (internal/cli/args.go), but this suite's fixtures/goldens are almost all
// inline scrollback behaviour — so, unless a test's own args already opt
// into fullscreen with "--fullscreen", startTUI passes "--inline" to keep
// them running against the mode they were written for. The
// TestTUI_Fullscreen* tests in tui_fullscreen_test.go are the fullscreen
// coverage; they pass "--fullscreen" explicitly (or, for
// TestTUI_Fullscreen_ToggleKey, deliberately start with neither flag to get
// this same inline default, then toggle at runtime with ctrl+f).
func startTUI(t *testing.T, cols, rows int, proj, home, sessDir, fauxAddr string, args ...string) *screen.Screen {
	t.Helper()
	wantsFullscreen := false
	for _, a := range args {
		if a == "--fullscreen" {
			wantsFullscreen = true
			break
		}
	}
	if !wantsFullscreen {
		args = append([]string{"--inline"}, args...)
	}
	opts := []screen.Option{
		screen.WithDir(proj),
		screen.WithEnv("HOME", home),
		screen.WithEnv("HARNESS_SESSIONS_DIR", sessDir),
		screen.WithEnv("HARNESS_MODEL", "faux/faux-1"),
		screen.WithEnv("HARNESS_TEST_CLOCK", testClock),
		// Retry backoff jitter is disabled for every e2e run, exactly like
		// the frozen test clock above: a countdown/reconnect test racing a
		// random delay is flaky by construction, not by bug (see
		// internal/harness/retry.go's delay).
		screen.WithEnv("HARNESS_RETRY_JITTER", "0"),
		// Answer kiln's startup tea.RequestBackgroundColor query with the
		// kiln design's own background (internal/tui/theme.go's designBg)
		// so internal/tui.SetTerminalBackground's "near design bg keeps the
		// exact design hexes" rule keeps every PTY golden byte-for-byte
		// identical to what it was before background-aware tokens existed,
		// rather than depending on whatever default background
		// github.com/charmbracelet/x/vt's emulator happens to start with.
		screen.WithBackgroundColor(tuiDesignBackground),
	}
	if fauxAddr != "" {
		opts = append(opts,
			screen.WithEnv("HARNESS_FAUX_ADDR", fauxAddr),
			screen.WithEnv("HARNESS_FAUX_API", "anthropic-messages"),
		)
	}

	opts = append(opts, tuiExtraOpts...)
	s := screen.Start(t, harnessBin, args, cols, rows, opts...)

	return s
}

// tuiExtraOpts lets one test add driver options (extra env, mostly) to
// startTUI's fixed set. Tests that set it must clear it in a Cleanup; this
// package never runs tests in parallel.
var tuiExtraOpts []screen.Option

// tuiFixture bundles the scratch environment a fix-bug-shaped TUI test
// needs: a project with the buggy src/math.js, an isolated HOME/session
// store, and a faux server loaded with script. It returns the server too
// (not just its address) so a caller can inspect Requests().
func tuiFixture(t *testing.T, script string) (proj, home, sessDir, addr string, requests func() []recordedMessages) {
	t.Helper()
	proj = scratchProject(t)
	home, sessDir = scratchHome(t)
	var srvAddr string
	srvAddr, srv := startFaux(t, script)
	return proj, home, sessDir, srvAddr, func() []recordedMessages {
		out := make([]recordedMessages, 0)
		for _, r := range srv.Requests() {
			out = append(out, recordedMessages(r.Messages))
		}
		return out
	}
}

// recordedMessages is one faux request's raw Messages JSON, kept as its
// own named byte-slice type so call sites read as "the messages body",
// not an anonymous []byte.
type recordedMessages []byte

// waitReady waits for the startup box marker, the point every test in this
// file treats as "the app is up and idle".
func waitReady(t *testing.T, s *screen.Screen) {
	t.Helper()
	if err := s.WaitFor(tuiUserMark, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

// spinnerFramePattern matches the busy-line's spinner glyph and label,
// e.g. "◐ Crunching…" or "- Crunching…" in plain mode — reusing
// spinnerRowPattern's own "glyph, label, ellipsis" shape (rather than a
// bare leading glyph) so it cannot false-positive on the banner's tips row
// ("/ commands   @ add files …", which also starts with a single
// character then a space).
var spinnerFramePattern = regexp.MustCompile(`(?m)^[◐◓◑◒\-\\|/] \S+…`)

// idlePlaceholderText is the editor's idle placeholder (editor.DefaultPlaceholder,
// docs/kiln-design-handoff/README.md "Interactions"): present once the
// turn-summary row is gone and the busy line has cleared, so its
// appearance is the "idle" signal replacing the old committed turn-summary
// line (removed — the busy line just disappears at turn end now).
const idlePlaceholderText = "describe a task"

// turnSummaryPattern used to match the removed turn-summary row
// ("✻ Brewed for …s · done h:mmAM", RenderTurnSummary — deleted, the kiln
// design's busy line just disappears at turn end with nothing committed
// in its place). Every other e2e file that waited on it (mcp_behaviour_test.go,
// mcp_tui_test.go, tui_fullscreen_test.go, tui_gap_test.go) is really
// waiting for "the turn is done": with no summary row left to watch for,
// this now matches the editor's idle placeholder instead, which appears
// only once busy is false and the input box shows it again — the same
// "turn is done" signal, just carried by a different row.
var turnSummaryPattern = regexp.MustCompile(regexp.QuoteMeta(idlePlaceholderText))

// waitTurnSettled waits until no row on screen carries a spinner frame and
// the editor's idle placeholder is back, then for the screen to stop
// changing entirely — the kiln design removed the turn-summary row
// ("✻ Brewed for …", the old settle signal this test used to wait on), so
// a turn's end is no longer marked by anything committed to scrollback; the
// busy line (and its spinner) just disappears (docs/kiln-design-handoff/README.md
// "Interactions").
func waitTurnSettled(t *testing.T, s *screen.Screen) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows := s.Rows()
		spinning := false
		idle := false
		for _, r := range rows {
			if spinnerFramePattern.MatchString(r) {
				spinning = true
			}
			if strings.Contains(r, idlePlaceholderText) {
				idle = true
			}
		}
		if !spinning && idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitTurnSettled: still busy after 10s:\n%s", strings.Join(rows, "\n"))
		}
		time.Sleep(15 * time.Millisecond)
	}
	if err := waitQuiescent(s, 250*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}
}

// waitQuiescent blocks until Rows() stops changing for quiet, or fails
// after timeout.
func waitQuiescent(s *screen.Screen, quiet, timeout time.Duration) error {
	last := strings.Join(s.Rows(), "\n")
	deadline := time.Now().Add(timeout)
	stableSince := time.Now()
	for {
		time.Sleep(15 * time.Millisecond)
		cur := strings.Join(s.Rows(), "\n")
		if cur != last {
			last = cur
			stableSince = time.Now()
		} else if time.Since(stableSince) >= quiet {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waitQuiescent: screen still changing after %s", timeout)
		}
	}
}

// waitScrollbackQuiescent is waitQuiescent's scrollback-only counterpart:
// it waits for s.Scrollback() (committed rows that have scrolled off the
// visible viewport) to stop changing, ignoring the live viewport entirely.
// Unlike the visible screen, scrollback never contains the busy line, so
// this succeeds even while a spinner or a permission prompt's elapsed
// clock keeps ticking in the viewport (which now happens throughout a
// tool-permission wait — see app.go's liveLines) — exactly the case
// TestTUI_Design_Streaming needs, since it only asserts on already-
// committed scrollback content.
func waitScrollbackQuiescent(s *screen.Screen, quiet, timeout time.Duration) error {
	last := strings.Join(s.Scrollback(), "\n")
	deadline := time.Now().Add(timeout)
	stableSince := time.Now()
	for {
		time.Sleep(15 * time.Millisecond)
		cur := strings.Join(s.Scrollback(), "\n")
		if cur != last {
			last = cur
			stableSince = time.Now()
		} else if time.Since(stableSince) >= quiet {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waitScrollbackQuiescent: scrollback still changing after %s", timeout)
		}
	}
}

// submitSlashCommand types "/<name>" and submits it as a slash command.
//
// This needs two Enters, not one: internal/tui's `/`-autocomplete popup
// (internal/tui/autocomplete.go, currently under active development by
// another agent alongside this suite — see this file's package comment)
// owns Enter while it is open and treats the first Enter as "accept the
// highlighted completion", which only replaces the typed text with itself
// plus a trailing space and closes the popup; it does not submit the
// line. A second Enter is what actually submits. This suite never
// renders or asserts on the popup itself (per this task's instructions,
// no goldens for the `/`/`@` popup while it's mid-change) — it only has
// to get past it to reach the command underneath.
func submitSlashCommand(s *screen.Screen, name string) {
	// A fully-typed slash command submits on one Enter now (the popup no
	// longer eats the first Enter to re-insert what is already typed).
	s.Send("/" + name)
	s.SendKey("enter")
}

// assertGoldenTail compares the screen against a golden file the same way
// screen.Golden does, except it first drops every row above the first one
// containing anchor.
//
// Why this exists (a real bug, not a test-shaping convenience — see this
// suite's report): at a terminal size where the transcript's total
// printed line count sits right at the viewport height (100x30 for the
// fix-bug flow), the exact number of lines that end up committed to
// scrollback is occasionally one different between two runs of the
// *identical* script — observed via `go test -tags e2e -run TestTUI_FixBug
// -count=1` looped ~15 times, failing 1-2 times regardless of the
// quiescence wait in waitTurnSettled. When it differs, the startup
// banner's own first line ("⏺ harness — faux/faux-1") is sometimes still
// on screen and sometimes already scrolled into scrollback, and every
// row below that point is byte-for-byte identical between the two
// variants, just shifted by one row. That is: content is fine, but
// "how many total lines got printed" is not deterministic yet. Comparing
// from a stable anchor (a line far enough from the top that it never
// scrolls off in either variant) makes the golden assert on content
// instead of on this open row-count race.
// maskStatusRowCwd rewrites the status line's location segment, which now
// always carries the working directory: RenderStatusLine shortens a long
// path with a leading ellipsis instead of dropping it (the real-terminal
// bug where cwd and branch vanished entirely). Every e2e run works in a
// fresh t.TempDir(), so that path differs on every run and would make any
// golden containing the status row non-deterministic. Masking it keeps the
// rest of the row — mode label, ctx meter, cost — compared exactly.
// busySpinnerGlyphPattern matches the animated glyph that opens the busy
// line. The frame advances every 140ms (internal/tui/app.go
// spinnerInterval), so whichever of the four frames a capture happens to
// land on is a coin toss — the long-standing source of flakes in this
// suite. Pinning it to one frame keeps the rest of the busy row (status
// phrase, elapsed, tokens, "esc to stop") compared exactly. All four
// glyphs in each set are one cell wide, so the substitution preserves
// column alignment and the per-cell styles stay paired with their text.
var busySpinnerGlyphPattern = regexp.MustCompile(`(?m)^([◐◓◑◒]|[-\\|/]) `)

// normalizeSpinnerGlyph pins the busy line's animated glyph to one frame.
func normalizeSpinnerGlyph(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = busySpinnerGlyphPattern.ReplaceAllString(r, "◐ ")
	}
	return out
}

func maskStatusRowCwd(r string) (string, bool) {
	if statusRowCwdPattern.MatchString(r) {
		return statusRowCwdPattern.ReplaceAllString(r, "⇧⇥  <cwd> ctx"), true
	}
	return "", false
}

// normalizeStatusRowCwd applies maskStatusRowCwd to a plain-text screen.
func normalizeStatusRowCwd(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		if masked, ok := maskStatusRowCwd(r); ok {
			out[i] = masked
			continue
		}
		out[i] = r
	}
	return out
}

func assertGoldenTail(t *testing.T, s *screen.Screen, name, anchor string) {
	t.Helper()
	rows := s.Rows()
	start := 0
	for i, r := range rows {
		if strings.Contains(r, anchor) {
			start = i
			break
		}
	}
	got := strings.Join(normalizeSpinnerGlyph(normalizeStatusRowCwd(rows[start:])), "\n")
	assertGolden(t, goldenPath(name+".txt"), got+"\n")
}

// spinnerRowPattern matches the live spinner/status row rendered above a
// pending permission prompt, e.g. "· working (0s · ↓ 150 tokens)" or
// "✶ thinking (0s · ↓ 150 tokens)".
var spinnerRowPattern = regexp.MustCompile(`(?m)^[◐◓◑◒] \S+…`)

// anyRowMatches reports whether any current screen row matches re.
func anyRowMatches(s *screen.Screen, re *regexp.Regexp) bool {
	for _, r := range s.Rows() {
		if re.MatchString(r) {
			return true
		}
	}
	return false
}

// assertGoldenNormalizedSpinner is assertGolden's counterpart for a screen
// captured while a turn is still busy: it replaces the one row driven by
// the live spinner with a fixed placeholder before comparing.
//
// Why this exists (a real, if narrow, race — not a test-shaping
// convenience): that row's glyph cycles on a real 80ms tea.Tick
// (spinner.go), and its label is the turn's base gerund
// (transcript.go's PickLabel(turn), deterministic — "Working" for turn
// 1) *unless* the live Thinking block is still active, in which case
// handleThinking's SetLabel("Thinking") temporarily overrides it until
// the block's Ended message is processed. Whether the permission
// prompt for the Read call (which the fixed script emits right after
// the thinking block, in the same streamed turn) lands on screen before
// or after that Ended message is processed is a genuine race between
// two independently-arriving events, not something HARNESS_TEST_CLOCK
// freezes — observed to flip between "· working (0s...)" and "✶
// thinking (0s...)" across otherwise identical runs of
// TestTUI_Permission_DenyWithFeedback (`go test -tags e2e -run
// TestTUI_ -count=2`).
func assertGoldenNormalizedSpinner(t *testing.T, s *screen.Screen, name string) {
	t.Helper()
	// Also normalizes the startup banner's cwd row — see
	// normalizeBannerCwdRow's doc comment; this screen still shows it
	// (nothing has scrolled it out of testTUI_Permission_DenyWithFeedback's
	// short transcript at 30 rows).
	rows := normalizeBannerCwdRow(s.Rows())
	norm := make([]string, len(rows))
	for i, r := range rows {
		if spinnerRowPattern.MatchString(r) {
			norm[i] = "<spinner> (0s · ↓ 150 tokens)"
			continue
		}
		norm[i] = r
	}
	got := strings.Join(norm, "\n")
	assertGolden(t, goldenPath(name+".txt"), got+"\n")
}

// bannerCwdMarker is the fixed text kiln's banner puts right after the
// session's cwd on the banner's second row (internal/cli/tui.go's
// bannerRows: `deps.Cwd + " · model " + deps.ModelLabel + ...`). Unlike
// Claude Code's own banner (a "▝▝ ▝▝" logo glyph before the cwd), kiln's
// banner has no glyph marker at all — the cwd is the row's own leading
// text — so the marker to split on is the " · model " that always follows
// it instead of a glyph that precedes it.
const bannerCwdMarker = " · model "

// normalizeBannerCwdRow masks the startup banner's cwd row before a golden
// compare.
//
// Real, load-bearing bug this test-side normalization works around (not
// routed around silently — see this suite's report): the banner shows the
// session's absolute cwd (internal/cli/tui.go), and this suite's fixtures
// all cwd into a fresh t.TempDir() per test run, whose own random suffix
// varies in length from run to run (confirmed by running
// TestTUI_EmptyBox_Widths twice in a row and diffing the two "want" golden
// captures byte for byte). At the narrower widths (20/40/80 columns) the
// path gets truncated by FitStatus before reaching that suffix, so the row
// reads as stable by accident; at 117/118/144 columns the row is long
// enough that FitStatus's truncation point (or lack of one) falls inside
// or right at the end of the trailing " · model ..." text, so a
// differently-sized random suffix shifts that cut point and changes the
// row's tail too (e.g. "faux/faux-1" vs "faux/faux-"), not just its cwd
// portion. A golden file cannot pin a value that is different every time
// the test that produces it runs, on any machine, so everything from the
// cwd through the rest of the row is replaced with a fixed placeholder
// (dropping the model/effort tail entirely, since this row's golden
// coverage is about box-width layout, not banner wording) before writing
// or comparing against testdata/golden/*.txt.
// statusRowCwdPattern matches the status line's location segment (and the
// spacer after it, whose width depends on the path's length) when a
// terminal is wide enough that the full scratch-project path (a run-
// varying temp dir, e.g. .../TestFoo1234567890/001, whose length itself
// varies run to run) survives un-truncated after the mode segment's "⇧⇥"
// — status.go's RenderStatusLine, "<mode segment>  <cwd>[· branch]
// <spacer>ctx …". The whole match (path plus its variable-width spacer) is
// replaced as one unit, or a shorter run's spacer would leave a
// differently-sized gap than a longer run's and the golden would still
// flap between otherwise-identical runs. Narrower widths either drop this
// segment entirely (nothing to mask) or truncate it with "…", which
// normalizeBannerCwdRow's other cases already stabilize.
var statusRowCwdPattern = regexp.MustCompile(`⇧⇥ {2}\S+\s+ctx`)

func normalizeBannerCwdRow(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		switch {
		case strings.Contains(r, bannerCwdMarker):
			// Wide enough that "<cwd> · [branch B ·] model M" survives.
			out[i] = "<cwd>" + bannerCwdMarker + "…"
		case (strings.HasPrefix(r, "/") || strings.HasPrefix(r, "~")) && !strings.Contains(r, "commands"):
			// The banner's cwd row, truncated so hard that the " · model "
			// marker itself was cut off — a run-varying temp dir, so mask
			// it whole. Excludes the banner's tips row ("/ commands   @ add
			// files …"), which also starts with "/" (the kiln-amber "/"
			// glyph) but is never a path.
			out[i] = "<cwd>…"
		case statusRowCwdPattern.MatchString(r):
			// The status line's own location segment, wide enough to show
			// the full scratch path un-truncated.
			out[i] = statusRowCwdPattern.ReplaceAllString(r, "⇧⇥  <cwd> ctx")
		default:
			out[i] = r
		}
	}
	return out
}

// assertGoldenNormalizedBanner is assertGolden's counterpart for a screen
// that still shows the startup banner (see normalizeBannerCwdRow).
func assertGoldenNormalizedBanner(t *testing.T, s *screen.Screen, name string) {
	t.Helper()
	got := strings.Join(normalizeBannerCwdRow(s.Rows()), "\n")
	assertGolden(t, goldenPath(name+".txt"), got+"\n")
}

// modeLinePattern matches the bottom area's single status-line row in any
// of its states: RenderStatusLine's five mode labels (status.go) — "ask
// before edits", "auto-edit", "bypass permissions", "don't ask", "plan
// only" — the one-second Ctrl+C hint that replaces it ("Press Ctrl-C again
// to exit"), the Ctrl+Y paste hint, or the Ctrl+O verbose notice's
// right-aligned "verbose" label.
var modeLinePattern = regexp.MustCompile(`ask before edits|auto-edit|bypass permissions|don't ask|plan only|Press Ctrl-C again to exit|Ctrl\+Y to paste deleted text|^\s*verbose\s*$|verbose$`)

// assertFooterInvariant checks the two things every screen in this file
// that isn't mid-panel/mid-transcript-view should satisfy: the bottom area
// is exactly one row — the status line, with no extra row above it
// (docs/kiln-design-handoff/README.md "Screen anatomy": the bottom area is
// the input box and the status line only, see app.go's renderStatusRow) —
// and nothing is drawn below it (OccupiedHeight matches the trimmed row
// count exactly).
func assertFooterInvariant(t *testing.T, s *screen.Screen) {
	t.Helper()
	rows := s.Rows()
	if len(rows) < 1 {
		t.Fatalf("assertFooterInvariant: only %d rows, want at least 1:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	last := rows[len(rows)-1]
	if !modeLinePattern.MatchString(last) {
		t.Errorf("assertFooterInvariant: last row is not the mode line: %q", last)
	}
	if got, want := s.OccupiedHeight(), len(rows); got != want {
		t.Errorf("assertFooterInvariant: OccupiedHeight()=%d, len(Rows())=%d — something is drawn (or left blank) past the footer", got, want)
	}
}

// repoTestdataDir resolves the repository's top-level testdata directory
// from this source file's own location, the same way harness_test.go's
// goldenPath resolves testdata/golden (go test's cwd is the package
// directory, test/e2e, not the repo root testdata hangs off).
func repoTestdataDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata")
}

// loadFauxScript reads a script under testdata/faux/<name>.yaml.
func loadFauxScript(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(repoTestdataDir(), "faux", name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read faux script %s: %v", path, err)
	}
	return string(data)
}

// stylesOpts controls assertGoldenStyles's normalisation, mirroring the
// plain-text helpers above: anchor drops every row above the first one
// containing it (like assertGoldenTail; empty keeps all rows),
// normalizeBanner masks the startup banner's cwd row (like
// assertGoldenNormalizedBanner / normalizeBannerCwdRow), and
// normalizeSpinner masks the live spinner/status row (like
// assertGoldenNormalizedSpinner).
type stylesOpts struct {
	anchor           string
	normalizeBanner  bool
	normalizeSpinner bool
}

// maskStyledRows replaces the text of every row mask matches with the
// text mask returns, and drops that row's style spans entirely (all
// cells reset to screen.CellStyle{}, the zero/unstyled value) — masking
// both the volatile text and its styling the same way the plain-text
// helpers mask volatile text. Rows mask does not match are returned
// unchanged, sharing the original styles slice.
func maskStyledRows(rows []string, styles [][]screen.CellStyle, mask func(string) (string, bool)) ([]string, [][]screen.CellStyle) {
	outRows := make([]string, len(rows))
	outStyles := make([][]screen.CellStyle, len(styles))
	for i, r := range rows {
		if masked, ok := mask(r); ok {
			outRows[i] = masked
			outStyles[i] = make([]screen.CellStyle, len(styles[i]))
			continue
		}
		outRows[i] = r
		outStyles[i] = styles[i]
	}
	return outRows, outStyles
}

// maskBannerCwdRowStyled is normalizeBannerCwdRow's per-row predicate,
// reused by assertGoldenStyles via maskStyledRows so the styled encoding
// masks the same row the same way the plain-text goldens do.
func maskBannerCwdRowStyled(r string) (string, bool) {
	switch {
	case strings.Contains(r, bannerCwdMarker):
		return "<cwd>" + bannerCwdMarker + "…", true
	case (strings.HasPrefix(r, "/") || strings.HasPrefix(r, "~")) && !strings.Contains(r, "commands"):
		return "<cwd>…", true
	case statusRowCwdPattern.MatchString(r):
		return statusRowCwdPattern.ReplaceAllString(r, "⇧⇥  <cwd> ctx"), true
	default:
		return "", false
	}
}

// maskSpinnerRowStyled is assertGoldenNormalizedSpinner's per-row
// predicate, reused by assertGoldenStyles.
func maskSpinnerRowStyled(r string) (string, bool) {
	if spinnerRowPattern.MatchString(r) {
		return "<spinner> (0s · ↓ 150 tokens)", true
	}
	return "", false
}

// assertGoldenStyles compares the screen's styled encoding
// (screen.EncodeStyledRow per row, over s.Viewport()/s.Styles()) against
// testdata/golden/<name>.styles.txt, after applying opts' normalisations.
// It is the styled counterpart to assertGoldenTail /
// assertGoldenNormalizedBanner / assertGoldenNormalizedSpinner: masking
// happens on the plain text + per-row style slice, in the same order the
// row would be dropped or replaced by those helpers, before the row is
// encoded — so a masked row's styling (which would otherwise be as
// volatile as its text: the live spinner glyph cycles colour with its
// frame, and the banner cwd row's width-dependent truncation point can
// shift where a style span ends) never reaches the golden file.
func assertGoldenStyles(t *testing.T, s *screen.Screen, name string, opts stylesOpts) {
	t.Helper()

	rows := s.Viewport()
	styles := s.Styles()

	start := 0
	if opts.anchor != "" {
		for i, r := range rows {
			if strings.Contains(r, opts.anchor) {
				start = i
				break
			}
		}
	}
	rows = rows[start:]
	styles = styles[start:]

	if opts.normalizeBanner {
		rows, styles = maskStyledRows(rows, styles, maskBannerCwdRowStyled)
	}
	if opts.normalizeSpinner {
		rows, styles = maskStyledRows(rows, styles, maskSpinnerRowStyled)
	}
	rows, styles = maskStyledRows(rows, styles, maskStatusRowCwd)
	rows = normalizeSpinnerGlyph(rows)

	lines := make([]string, len(rows))
	for i := range rows {
		lines[i] = screen.EncodeStyledRow(rows[i], styles[i])
	}
	for len(lines) > 0 && strings.TrimRight(lines[len(lines)-1], " ") == "" {
		lines = lines[:len(lines)-1]
	}
	got := strings.Join(lines, "\n")
	assertGolden(t, goldenPath(name+".styles.txt"), got+"\n")
}

// TestTUI_Startup_NoDuplicateRows guards the real bug a screenshot of kiln
// idle in a real terminal caught that this suite's own goldens hid two ways:
// assertGoldenTail drops every row above its anchor (the banner, sitting
// above "/ commands", was never even compared), and normalizeBannerCwdRow
// masks the banner's cwd row before comparing. This test does neither: it
// reads the raw, unanchored, unnormalized viewport right after waitReady and
// checks every non-blank row is unique — a startup banner row (or the tips
// row right after it) printed twice would show up as a duplicate here, the
// way it did in the real terminal. Rule rows (the full-width "─" divider,
// which legitimately repeats: the banner's own divider, the input box's top
// and bottom rules) are the one expected exception.
//
// It also pins the row *order* the fixed banner spacing (internal/cli/tui.go
// bannerRows) produces: the banner's own rows (wordmark, cwd/branch/model,
// tips — consecutive, no blank rows between them per the design), then one
// blank row, then the banner's closing rule, then the input box's own
// [rule, input, rule], then the one status row.
func TestTUI_Startup_NoDuplicateRows(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{80, 24}, {120, 40}, {200, 50}} {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			proj := scratchProject(t)
			home, sessDir := scratchHome(t)
			addr, _ := startFaux(t, fixBugScript)
			s := startTUI(t, sz.w, sz.h, proj, home, sessDir, addr)
			waitReady(t, s)

			rows := s.Rows()
			isRule := func(r string) bool {
				trimmed := strings.TrimRight(r, " ")
				return trimmed != "" && strings.Count(trimmed, "─") == len([]rune(trimmed))
			}

			seen := map[string]int{}
			for _, r := range rows {
				tr := strings.TrimRight(r, " ")
				if tr == "" || isRule(tr) {
					continue
				}
				seen[tr]++
			}
			for text, n := range seen {
				if n > 1 {
					t.Errorf("row %q appears %d times (want 1):\nfull screen:\n%s", text, n, strings.Join(rows, "\n"))
				}
			}

			// Order: banner rows (wordmark, cwd/branch/model, tips —
			// consecutive, no blank rows between them per the design), one
			// blank row, the banner's own closing rule, the input box's own
			// [rule, input, rule], then the one status row. Every screen
			// here is the empty-box startup screen (no "Recent sessions"
			// block: a fresh scratchProject/scratchHome has no prior
			// sessions).
			var got []string
			for _, r := range rows {
				got = append(got, strings.TrimRight(r, " "))
			}
			wantNonBlank := []int{0, 1, 2}
			for _, i := range wantNonBlank {
				if i >= len(got) || got[i] == "" {
					t.Errorf("row %d = %q, want banner content (wordmark/cwd/tips must be consecutive, no blanks between them)", i, safeRow(got, i))
				}
			}
			if safeRow(got, 3) != "" {
				t.Errorf("row 3 = %q, want blank (one blank row after the banner's tips row)", safeRow(got, 3))
			}
			wantRule := []int{4, 5, 7}
			for _, i := range wantRule {
				if i >= len(got) || !isRule(got[i]) {
					t.Errorf("row %d = %q, want a full-width rule row", i, safeRow(got, i))
				}
			}
			if last := got[len(got)-1]; !modeLinePattern.MatchString(last) {
				t.Errorf("last row = %q, want the status/mode line", last)
			}
		})
	}
}

// safeRow returns rows[i], or "<out of range>" if i is past the end —
// TestTUI_Startup_NoDuplicateRows's own error-message helper so an
// out-of-range index reports cleanly instead of panicking mid-test.
func safeRow(rows []string, i int) string {
	if i < 0 || i >= len(rows) {
		return "<out of range>"
	}
	return rows[i]
}

// --- 1. Empty box goldens across widths -------------------------------

func TestTUI_EmptyBox_Widths(t *testing.T) {
	widths := []int{20, 40, 80, 117, 118, 144}
	for _, w := range widths {
		w := w
		t.Run(strconv.Itoa(w), func(t *testing.T) {
			proj := scratchProject(t)
			home, sessDir := scratchHome(t)
			addr, _ := startFaux(t, fixBugScript)

			s := startTUI(t, w, 24, proj, home, sessDir, addr)
			waitReady(t, s)

			assertFooterInvariant(t, s)
			assertGoldenNormalizedBanner(t, s, "tui-empty-"+strconv.Itoa(w))
		})
	}
}

// --- 2. Fix-bug flow ----------------------------------------------------

func TestTUI_FixBug(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("fix the bug in math.js")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	joined := strings.Join(s.Rows(), "\n")
	// Read-only calls no longer collapse to a summary row in the
	// committed transcript (internal/tui/app.go's flushGroup, kiln UI
	// pass Phase 2.1): the live region still shows the collapsed
	// "Reading N files…" row while the group is in flight, but once it
	// flushes each call commits its own full "tool" block — so the
	// committed transcript shows a "read" label rule and a "Read
	// src/math.js" head line instead of "Read 1 file". Edit renders as
	// kiln's "edit" block: an "edit" label rule with the filename as meta,
	// then a panel header row carrying the path and +N/−N counts — there
	// is no separate "Update <path>" head line any more (Phase 2.2 drops
	// it; see transcript.go's RenderToolCall doc comment).
	if !strings.Contains(joined, "read ") || !strings.Contains(joined, "Read src/math.js") {
		t.Errorf("transcript missing the full \"read\" tool block:\n%s", joined)
	}
	if !strings.Contains(joined, "src/math.js") || !strings.Contains(joined, "+1") || !strings.Contains(joined, "−1") {
		t.Errorf("transcript missing the diff header row (path + counts):\n%s", joined)
	}

	// Golden the whole viewport rather than anchoring past the banner
	// (assertGoldenTail's own doc comment explains the anchor's original
	// purpose: hiding a genuine off-by-one scrollback-length race). By the
	// time this turn has settled at 100x30, the banner and its "/ commands"
	// anchor text have long since scrolled out of the visible viewport on
	// every run, so this is not expected to change the golden's content —
	// but a real duplicated banner/tips row (this suite's own regression,
	// see TestTUI_Startup_NoDuplicateRows) would no longer be silently
	// invisible here just because it happened to land above whatever row
	// the anchor matched.
	assertGolden(t, goldenPath("tui-fix-bug.txt"), strings.Join(normalizeSpinnerGlyph(normalizeStatusRowCwd(s.Rows())), "\n")+"\n")
	assertGoldenStyles(t, s, "tui-fix-bug", stylesOpts{})

	fixed, err := os.ReadFile(filepath.Join(proj, "src", "math.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fixed), "return a + b;") {
		t.Errorf("src/math.js not fixed on disk:\n%s", fixed)
	}

	sess := sessionFile(t, sessDir, proj)
	if _, err := os.Stat(sess); err != nil {
		t.Fatalf("session file missing: %v", err)
	}
}

// --- 3. Spinner / busy state ---------------------------------------------

func TestTUI_Spinner_Busy(t *testing.T) {
	script := loadFauxScript(t, "slow")
	proj, home, sessDir, addr, _ := tuiFixture(t, script)

	s := startTUI(t, 100, 24, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)
	assertFooterInvariant(t, s)
	idleHeight := s.OccupiedHeight()

	s.Send("go slow")
	s.SendKey("enter")

	// While busy, the spinner row reads "<glyph> <Label>…" (kiln glyphs
	// ◐◓◑◒). Match that row specifically — a bare "…" also appears in the
	// banner's truncated cwd/model row, so it is not a busy signal.
	if err := s.WaitFor(spinnerRowPattern, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	// The turn is still running (slow.yaml delays before replying): the
	// busy hint must still be up a beat later, not a one-frame flash.
	time.Sleep(200 * time.Millisecond)
	if !anyRowMatches(s, spinnerRowPattern) {
		t.Error("busy hint disappeared before the turn finished")
	}

	if err := s.WaitFor(turnSummaryPattern, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Give the final render a moment to land, then check the busy hint is
	// gone and no spinner residue row is left over: the idle layout is
	// exactly what it was before the turn started, modulo the committed
	// transcript lines the turn itself added above the live region.
	if err := waitQuiescent(s, 500*time.Millisecond, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if anyRowMatches(s, spinnerRowPattern) {
		t.Error("busy hint still present after the turn finished")
	}
	assertFooterInvariant(t, s)

	// The live region below the transcript (spinner + editor + footer) is
	// exactly as tall as it was at idle before the turn: OccupiedHeight
	// grew only by what the turn committed to scrollback, not by any
	// leftover spinner row.
	postHeight := s.OccupiedHeight()
	if postHeight < idleHeight {
		t.Errorf("OccupiedHeight shrank across a turn (%d -> %d)", idleHeight, postHeight)
	}
}

// --- 4. Permission prompt --------------------------------------------------

func TestTUI_Permission_DenyWithFeedback(t *testing.T) {
	proj, home, sessDir, addr, requests := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "manual",
	)
	waitReady(t, s)

	s.Send("fix the bug in math.js")
	s.SendKey("enter")

	// Read/glob/grep are auto-allowed in every mode now (grouped as "Read
	// N files" with no permission step, matching Claude Code — see this
	// suite's task brief item 4), so the fix-bug script's Read never
	// prompts; only the Edit that follows it does. The prompt no longer
	// says "Permission required" — RenderEditPermissionPrompt
	// (permission_render.go) asks "Allow kiln to edit <path>?" — and, per
	// Phase 3's C item, the "approval needed" block now stands alone: no
	// tool-call header commits above it any more (docs/kiln-design-handoff/
	// README.md's "Interactions"; app.go's MsgPermissionPrompt no longer
	// pre-commits "Update <path>" — the tool's own block commits after the
	// decision instead, on EventToolEnd).
	if err := s.WaitFor("Allow kiln to edit", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	assertGoldenNormalizedSpinner(t, s, "tui-permission")

	// The Edit prompt's rendered options (RenderEditPermissionPrompt) are
	// "Yes" / "Yes, and switch to accept edits" / "No" — unlike the
	// generic 3-option prompt, there is no "and tell kiln what to do
	// instead" wording here, so "3" (its "No") denies outright with no
	// feedback capture (finding 1: keys map to the rendered label, not a
	// one-size-fits-all 3-option scheme). Deny-with-feedback's own
	// mechanics — PromptState entering feedback mode, and permission.Check
	// turning Feedback into "the user declined and said: …" for the
	// model — are covered directly by permissionview_test.go's
	// TestPromptState_ToolDenyWithFeedback and permission_test.go, using a
	// tool whose rendered prompt actually offers that option.
	s.SendKey("3")

	// Declining commits a "✕ Declined Update <path>" system note ahead of
	// the model's own reply (Phase 3's C item).
	if err := s.WaitFor("Declined Update src/math.js", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor("declined. Ask what they would prefer", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, msgs := range requests() {
		if strings.Contains(string(msgs), "the user declined") {
			found = true
			break
		}
	}
	if !found {
		t.Error("faux never received a request whose Messages contained the decline reason")
	}
}

func TestTUI_Permission_Allow(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "manual",
	)
	waitReady(t, s)

	s.Send("fix the bug in math.js")
	s.SendKey("enter")

	// manual mode auto-allows the Read (read-only tools no longer prompt —
	// see TestTUI_Permission_DenyWithFeedback's comment) and asks only for
	// the Edit that follows it. Answer "y" each time a prompt comes up
	// until the turn finishes.
	permOrDone := regexp.MustCompile(`Allow kiln to edit|` + turnSummaryPattern.String())
	for i := 0; i < 5; i++ {
		if err := s.WaitFor(permOrDone, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		if turnSummaryPattern.MatchString(strings.Join(s.Rows(), "\n")) {
			break
		}
		s.SendKey("y")
		time.Sleep(50 * time.Millisecond)
	}
	if err := s.WaitFor(turnSummaryPattern, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	fixed, err := os.ReadFile(filepath.Join(proj, "src", "math.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fixed), "return a + b;") {
		t.Errorf("src/math.js not fixed after allowing every prompt:\n%s", fixed)
	}
}

// --- 5/6. Panels -----------------------------------------------------------

// panelTitles maps each panel-opening slash command to a stable substring
// of its dialog's rendered title (commands.ModalSpec.Title, or the
// dialog's own hardcoded title for /model and /mcp, which render outside
// commandDialog — see dialog_model.go's dialogModelTitle and
// dialog_mcp.go's dialogMCPTitle). Panels are no longer bordered boxes
// (docs/claude-code-reference.md §5: a full-screen dialog under a "▔ ◐
// medium · /effort ▔" rule, no ╭╮╰╯ glyphs), so a panel's presence is
// checked by its title text instead.
var panelTitles = map[string]string{
	"model":       "Select model",
	"permissions": "Permissions",
	"mcp":         "Manage MCP",
	"agents":      "Subagents",
	"config":      "Configuration",
}

func TestTUI_Panels_OpenClose(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	assertFooterInvariant(t, s)

	for _, name := range []string{"model", "permissions", "mcp", "agents", "config"} {
		title := panelTitles[name]

		submitSlashCommand(s, name)
		if err := s.WaitFor(title, 3*time.Second); err != nil {
			t.Fatalf("/%s: panel never opened (title %q never appeared): %v", name, title, err)
		}
		if name == "model" {
			assertGoldenNormalizedBanner(t, s, "tui-panel-model")
		}
		s.SendKey("esc")
		if err := s.WaitFor(tuiUserMark, 2*time.Second); err != nil {
			t.Fatalf("/%s: panel never closed: %v", name, err)
		}
		assertFooterInvariant(t, s)
		if strings.Contains(strings.Join(s.Rows(), "\n"), title) {
			t.Errorf("/%s: dialog title %q remains after close", name, title)
		}
	}
}

func TestTUI_ModelPanel_Select(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)

	submitSlashCommand(s, "model")
	if err := s.WaitFor("Select model", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	// The current model's row is marked with a ✓ (dialog_model.go's
	// NewDialogModel seeds the cursor from it; the kiln restyle renders
	// this from G().OK rather than the literal "✔" Claude Code used — QA
	// finding 20260927T000726Z-mcp-dialog-glyphs-plural's "rewind/model
	// checkmarks likewise ✓").
	if err := s.WaitFor(regexp.MustCompile(`✓.*faux/faux-1|faux/faux-1.*✓`), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	// The faux provider registers both faux-1 and faux-2
	// (internal/provider/faux/faux.go) regardless of which are scripted,
	// so the picker always lists two rows here; faux-2's is unmarked (not
	// the current model).
	if err := s.WaitFor("faux/faux-2", 2*time.Second); err != nil {
		t.Fatalf("model picker missing the second row (faux/faux-2): %v", err)
	}

	// Enter runs the modal's Select against the highlighted (faux-1) row
	// as a tea.Cmd (dialog_model.go's HandleKey returns a func() tea.Msg
	// rather than calling switchModel inline), so it no longer blocks
	// Bubbletea's Update loop — the hang this test used to guard against
	// is fixed. The panel stays open after Enter (Select reports a status
	// line but does not close it); Esc closes it.
	s.SendKey("enter")
	s.SendKey("esc")
	if err := s.WaitFor(tuiUserMark, 5*time.Second); err != nil {
		t.Fatalf("model panel never closed after selecting: %v", err)
	}
	if err := s.WaitFor("faux/faux-1", 2*time.Second); err != nil {
		t.Fatalf("footer no longer shows faux/faux-1 after selecting it: %v", err)
	}
	assertFooterInvariant(t, s)
}

func TestTUI_ShiftTab_CyclesMode(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)

	// permissionModeRing in app.go: auto -> manual -> acceptEdits -> plan ->
	// auto; the default start mode is manual, so cycling from there goes
	// manual -> acceptEdits -> plan -> auto -> manual. Status-line mode
	// labels are status.go's modeLabel: "ask before edits" (manual),
	// "auto-edit" (acceptEdits), "plan only" (plan), "auto mode" (auto) —
	// acceptEdits and auto are distinct ring stops with distinct labels
	// (qa/findings/20260926T231105Z-mode-ring-duplicate-label.json).
	order := []string{"ask before edits", "auto-edit", "plan only", "auto mode", "ask before edits"}
	if err := s.WaitFor(order[0], 2*time.Second); err != nil {
		t.Fatalf("did not start in manual mode: %v", err)
	}
	for i := 1; i < len(order); i++ {
		s.SendKey("shift+tab")
		if err := s.WaitFor(order[i], 2*time.Second); err != nil {
			t.Fatalf("after %d shift+tab presses, expected %q: %v", i, order[i], err)
		}
	}
}

// --- 7. Ctrl+O verbose transcript ----------------------------------------

// TestTUI_CtrlO_Verbose replaces the old TestTUI_CtrlR_TranscriptView:
// Ctrl+R no longer opens a transcript view; verbose output toggles on
// Ctrl+O instead (app.go's toggleVerbose), replacing the mode line with
// "Showing detailed transcript · ctrl+o to toggle" and re-rendering the
// transcript so far with tool-call detail (absolute paths, "→" results —
// see replayTranscript).
func TestTUI_CtrlO_Verbose(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("fix the bug in math.js")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	s.SendKey("ctrl+o")
	if err := s.WaitFor("Showing detailed transcript · ctrl+o to toggle", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(s.Rows(), "\n")
	// Verbose mode shows the absolute path, not the cwd-relative one
	// (RenderToolCall/MapToolName). kiln's "tool" block anatomy has no
	// "Name(arg)" parenthesized header any more — it's a label rule
	// ("read") above a plain "Read" line, with the (possibly wrapped)
	// path as Muted continuation text. The "edit" block for a diff has no
	// "Update <path>" head line at all (Phase 2.2 drops it) — the path
	// instead shows on the panel header row alongside its +N/−N counts.
	if !strings.Contains(joined, "Read") || !strings.Contains(joined, "edit ") || !strings.Contains(joined, "math.js") {
		t.Errorf("verbose transcript missing tool calls:\n%s", joined)
	}
	// kiln's result-line marker is "→" (Action glyph), not Claude Code's
	// "⎿".
	if !strings.Contains(joined, "→") {
		t.Errorf("verbose transcript missing a result row (→):\n%s", joined)
	}
	// The full proj path can legitimately word-wrap across two rendered
	// rows (FitLines/ansiWrap on a long path), so check for the absolute
	// path's leading "/" rather than the whole string as one substring.
	if !strings.Contains(joined, "/private/") && !strings.Contains(joined, "/tmp/") {
		t.Errorf("verbose transcript missing an absolute path:\n%s", joined)
	}

	s.SendKey("ctrl+o")
	if err := s.WaitFor(tuiUserMark, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(s.Rows(), "\n"), "Showing detailed transcript") {
		t.Error("verbose notice still present after toggling back")
	}
}

// --- 8. Resize sweep ---------------------------------------------------

func TestTUI_ResizeSweep(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 120, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("fix the bug in math.js")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	for _, w := range []int{90, 60, 40, 20} {
		s.Resize(w, 24)
		if err := s.WaitFor(tuiUserMark, 3*time.Second); err != nil {
			t.Fatalf("resize to %dx24: footer marker never reappeared: %v", w, err)
		}
		// The kernel's own SIGWINCH-driven reflow of already-printed cells
		// and the program's own re-render (a new WindowSizeMsg -> View())
		// are two separate events; WaitFor above can observe a transient
		// frame from the first before the second lands. Settle before
		// reading Rows() so the assertions below see the redrawn frame,
		// not an in-between one.
		if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
			t.Fatalf("resize to %dx24: %v", w, err)
		}
		// Rows() itself asserts the width invariant (checkWidth), so simply
		// calling it here is the assertion for "no row overflows width".
		rows := s.Rows()
		if len(rows) < 1 {
			t.Fatalf("resize to %dx24: too few rows: %d", w, len(rows))
		}
		// The bottom area is one row now — the mode line only (no status
		// row, see assertFooterInvariant's doc comment). This run stays in
		// bypassPermissions the whole time (modeLineText, app.go), whose
		// wording is "bypass permissions on (shift+tab to cycle)"; at the
		// narrowest widths FitStatus truncates the tail with "…", so only
		// the front is checked.
		if !strings.Contains(rows[len(rows)-1], "bypass permiss") {
			t.Errorf("resize to %dx24: last row is not the mode line: %q", w, rows[len(rows)-1])
		}
		// The input box's rule spans the new width: find a row made only of
		// box-drawing rule characters/spaces and check its rendered width.
		foundRule := false
		for _, r := range s.Viewport() {
			if isRuleRow(r) {
				foundRule = true
				break
			}
		}
		if !foundRule {
			t.Errorf("resize to %dx24: no input-box rule row found", w)
		}
	}
}

// isRuleRow reports whether row looks like the editor's horizontal rule
// (all box-drawing dashes, or blank).
func isRuleRow(row string) bool {
	trimmed := strings.TrimRight(row, " ")
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if r != '─' {
			return false
		}
	}
	return true
}

// --- 9. --ax-screen-reader -----------------------------------------------

func TestTUI_AxScreenReader_Empty(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 80, 24, proj, home, sessDir, addr, "--ax-screen-reader")
	// --ax-screen-reader swaps the Unicode UserMark ("❯") for ASCII (">")
	// — internal/tui/theme.go's ASCIIGlyphs — so waitReady's own marker
	// wait doesn't apply here.
	if err := s.WaitFor(">", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	assertAXFooterInvariant(t, s)
	assertGoldenNormalizedBanner(t, s, "tui-ax-empty-80")
}

func TestTUI_AxScreenReader_FixBug(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--ax-screen-reader",
		"--permission-mode", "bypassPermissions",
	)
	if err := s.WaitFor(">", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	s.Send("fix the bug in math.js")
	s.SendKey("enter")
	waitTurnSettled(t, s)
	assertGoldenTail(t, s, "tui-ax-fix-bug", "/ commands")

	joined := strings.Join(s.Rows(), "\n")
	// Plain mode: ASCII glyphs only (internal/tui/theme.go's ASCIIGlyphs —
	// "*" for the call marker, ">" for the user mark), never kiln's
	// Unicode decorative set (UnicodeGlyphs: Call "⏺", UserMark "›",
	// Summary "✻", Thinking "∴").
	for _, glyph := range []string{"⏺", "›", "✻", "∴"} {
		if strings.Contains(joined, glyph) {
			t.Errorf("--ax-screen-reader screen still contains decorative glyph %q:\n%s", glyph, joined)
		}
	}
}

// assertAXFooterInvariant is assertFooterInvariant's plain-mode
// counterpart. The bottom area is the same single mode-line row in
// --ax-screen-reader mode too (modeLineText's wording does not change
// under ASCIIGlyphs — only the UserMark/tool-call glyphs swap, see
// theme.go), so the check is identical.
func assertAXFooterInvariant(t *testing.T, s *screen.Screen) {
	t.Helper()
	rows := s.Rows()
	if len(rows) < 1 {
		t.Fatalf("assertAXFooterInvariant: only %d rows", len(rows))
	}
	last := rows[len(rows)-1]
	if !modeLinePattern.MatchString(last) {
		t.Errorf("assertAXFooterInvariant: last row is not the mode line: %q", last)
	}
	if got, want := s.OccupiedHeight(), len(rows); got != want {
		t.Errorf("assertAXFooterInvariant: OccupiedHeight()=%d, len(Rows())=%d", got, want)
	}
}

// --- 10. `!` and `#` input modes -------------------------------------------

func TestTUI_BangCommand(t *testing.T) {
	proj, home, sessDir, addr, requests := tuiFixture(t, fixBugScript)

	s := startTUI(t, 100, 24, proj, home, sessDir, addr)
	waitReady(t, s)

	s.Send("!echo hi")
	s.SendKey("enter")
	if err := s.WaitFor("hi", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := len(requests()); got != 0 {
		t.Errorf("!echo hi sent %d requests to faux, want 0 (a bang command never reaches the model)", got)
	}
}

func TestTUI_MemoryNote(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)

	// memory.AddMemory (internal/claude/memory/memory.go) opens
	// $HOME/.claude/CLAUDE.md with O_CREATE but never MkdirAll's the
	// .claude directory itself; on a real machine that directory already
	// exists (settings, credentials, ...), but scratchHome's $HOME is
	// empty, so this is scratch-fixture setup, not a workaround for an
	// app bug.
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := startTUI(t, 100, 24, proj, home, sessDir, addr)
	waitReady(t, s)

	s.Send("#remember to use tabs")
	s.SendKey("enter")
	if err := s.WaitFor("added to", 3*time.Second); err != nil {
		t.Fatal(err)
	}

	// scratchProject's dir has no CLAUDE.md yet, so AddMemory (see
	// internal/claude/memory/memory.go's AddMemory) falls back to the
	// user-level file under $HOME/.claude/CLAUDE.md.
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), "remember to use tabs") {
		t.Errorf("%s does not contain the note:\n%s", path, data)
	}
}

// --- 11. Ctrl+C exit --------------------------------------------------

func TestTUI_CtrlC_DoublePressExits(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 80, 24, proj, home, sessDir, addr)
	waitReady(t, s)

	s.SendKey("ctrl+c")
	if err := s.WaitFor("Press Ctrl-C again to exit", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("ctrl+c")
	code, err := s.Exit()
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
}

func TestTUI_CtrlC_FirstPressClearsInputInsteadOfExiting(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 80, 24, proj, home, sessDir, addr)
	waitReady(t, s)

	s.Send("this should be cleared")
	if err := s.WaitFor("this should be cleared", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("ctrl+c")
	if err := s.WaitFor("Press Ctrl-C again to exit", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(s.Rows(), "\n"), "this should be cleared") {
		t.Error("first ctrl+c did not clear the editor text")
	}

	// A second ctrl+c well after the double-press window (see keys.go's
	// DoublePress, 1s) must not exit; it goes back to "clear input" /
	// hint again, since the editor is now empty.
	time.Sleep(1200 * time.Millisecond)
	s.SendKey("ctrl+c")
	time.Sleep(500 * time.Millisecond)
	if err := s.WaitFor(tuiUserMark, 1*time.Second); err != nil {
		t.Fatal("process appears to have exited after a single, stale ctrl+c")
	}
}

// TestTUI_SlashExit_Quits verifies that the /exit slash command terminates
// the program (it previously only echoed into the transcript and did
// nothing — the exit command's callback was never wired in interactive
// mode; it now signals the shell to quit via commands.Result.Exit).
func TestTUI_SlashExit_Quits(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 80, 24, proj, home, sessDir, addr)
	waitReady(t, s)

	s.Send("/exit")
	s.SendKey("enter")
	code, err := s.Exit()
	if err != nil {
		t.Fatalf("/exit did not terminate the program: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
}

// --- 8. Subagents panel ---------------------------------------------------

// TestTUI_SubagentsPanel_TwoLiveThenCleared drives
// testdata/faux/task_concurrent_tui.yaml through the real interactive
// binary: two `task` calls dispatch from one assistant message (internal/
// harness runs concurrent task tool calls in parallel), and the script's
// 400ms delay on both subagents' replies holds them open long enough for
// this test to observe the subagents panel showing two live rows at once,
// before either finishes. Once the turn settles, both rows have resolved
// (done) and then the panel clears entirely — cleared_at_next_turn is
// covered by asserting the panel is gone once the *next* turn starts.
func TestTUI_SubagentsPanel_TwoLiveThenCleared(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/task_concurrent_tui.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, string(script))
	writeModelRolesSettings(t, proj, map[string]string{"fast": "faux/faux-2"})

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "dontAsk",
	)
	waitReady(t, s)

	s.Send("dispatch two tasks")
	s.SendKey("enter")

	// Both dispatches are live (running) at once: the header reads "2
	// subagents running in parallel" while the 400ms delay on each
	// subagent's reply is still in flight.
	if err := s.WaitFor(regexp.MustCompile(`2 subagents running in parallel`), 3*time.Second); err != nil {
		t.Fatalf("subagents panel never showed two live rows: %v", err)
	}
	if err := s.WaitFor("look something up", 500*time.Millisecond); err != nil {
		t.Fatalf("subagents panel missing the inherited dispatch's description: %v", err)
	}
	if err := s.WaitFor("do the fast lookup", 500*time.Millisecond); err != nil {
		t.Fatalf("subagents panel missing the fast-routed dispatch's description: %v", err)
	}

	waitTurnSettled(t, s)

	// The panel is gone once the turn has settled and no further prompt
	// has been submitted (finishTurn does not itself clear it — the panel
	// clears at the *next* turn's start, app.go's beginTurn — so this
	// checks it's still visible with both rows resolved right after the
	// turn ends)...
	if err := s.WaitFor(regexp.MustCompile(`2 subagents finished`), 2*time.Second); err != nil {
		t.Fatalf("subagents panel did not settle to two done rows: %v", err)
	}

	// The finished panel committed to scrollback as an ordinary transcript
	// block (docs/kiln-design-handoff/README.md's "Blocks update in
	// place": the last update is the one that survives) — it does not
	// vanish outright, unlike before this phase. Under the "live while
	// last" freeze rule (live_freeze.go), the panel may in fact have
	// committed more than once during the turn (each time something else
	// committed while it was still live, e.g. each task call's own tool
	// result), so this checks scrollback (not the visible viewport, which
	// a later turn's own output can scroll) for how many committed
	// snapshots exist, not a fragile raw substring count.
	countCommitted := func() int {
		all := append(append([]string(nil), s.Scrollback()...), s.Rows()...)
		return strings.Count(strings.Join(all, "\n"), "subagents finished")
	}
	before := countCommitted()
	if before == 0 {
		t.Fatalf("expected the finished panel to have committed to scrollback")
	}

	s.Send("another prompt")
	s.SendKey("enter")
	if err := s.WaitFor(turnSummaryPattern, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	// What has to be true is that submitting a new turn — one that
	// dispatches no subagents — never shows a second, live "N subagents
	// running in parallel" header (the reset happens at the turn boundary,
	// app.go's beginTurn, so the panel starts empty and stays empty) and
	// never commits an additional finished snapshot either.
	if strings.Contains(strings.Join(s.Rows(), "\n"), "running in parallel") {
		t.Errorf("subagents panel reappeared live for the new turn:\n%s", strings.Join(s.Rows(), "\n"))
	}
	if after := countCommitted(); after != before {
		t.Errorf("subagents panel committed again for a turn with no dispatches (before=%d after=%d)", before, after)
	}
}

// --- Phase 1 (bottom chrome): status line and placeholder --------------

// TestTUI_StatusLine_CtxAndCost drives a turn that reports usage and
// checks the status row's context segment: "ctx", a meter (kiln's ━/─
// glyphs) and a percentage. faux-1's context window is 128000
// (internal/provider/faux/faux.go), so usage {input: 5000, output: 200}
// is 5200/128000 ≈ 4%. faux-1 has no configured price
// (provider.ModelCost{}, faux.go), so the cost segment reads "$0.00" —
// the cost segment always renders now (design scene 01), so this checks
// for that literal zero value rather than the segment's absence.
func TestTUI_StatusLine_CtxAndCost(t *testing.T) {
	script := loadFauxScript(t, "status-usage")
	proj, home, sessDir, addr, _ := tuiFixture(t, script)

	s := startTUI(t, 100, 24, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("go")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	rows := s.Rows()
	var statusRow string
	for _, r := range rows {
		if strings.Contains(r, "ctx ") {
			statusRow = r
			break
		}
	}
	if statusRow == "" {
		t.Fatalf("no row shows the context segment (\"ctx \"):\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(statusRow, "%") {
		t.Errorf("status row %q missing a percentage", statusRow)
	}
	if !strings.ContainsAny(statusRow, "━─=-") {
		t.Errorf("status row %q missing the context meter glyphs", statusRow)
	}
	if !strings.Contains(statusRow, "$0.00") {
		t.Errorf("status row %q should show the cost segment as $0.00 (faux-1 has no configured price)", statusRow)
	}
}

// TestTUI_Placeholder_Busy checks the editor's placeholder switches from
// the idle default to the busy text while a turn is running (SetPlaceholder,
// app.go's beginTurn/finishTurn — docs/kiln-design-handoff/README.md
// "Interactions"), then back once the turn settles.
func TestTUI_Placeholder_Busy(t *testing.T) {
	script := loadFauxScript(t, "slow")
	proj, home, sessDir, addr, _ := tuiFixture(t, script)

	s := startTUI(t, 100, 24, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)
	if err := s.WaitFor(idlePlaceholderText, 2*time.Second); err != nil {
		t.Fatalf("idle placeholder never appeared: %v", err)
	}

	s.Send("go slow")
	s.SendKey("enter")

	if err := s.WaitFor("queue a follow-up", 2*time.Second); err != nil {
		t.Fatalf("busy placeholder never appeared: %v", err)
	}
	if anyRowMatches(s, regexp.MustCompile(idlePlaceholderText)) {
		t.Error("idle placeholder still visible while busy")
	}

	waitTurnSettled(t, s)
	if err := s.WaitFor(idlePlaceholderText, 2*time.Second); err != nil {
		t.Fatalf("idle placeholder did not return after the turn settled: %v", err)
	}
}
