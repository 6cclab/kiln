package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tui/editor"
)

// spinnerInterval is the frame interval for the busy-line spinner: kiln's
// ◐◓◑◒ animates at 140ms/frame (design_handoff_kiln_tui/README.md), not
// the Claude Code original's 80ms.
const spinnerInterval = 140 * time.Millisecond

// permissionModeRing is the Shift+Tab cycle order (parity spec:
// docs/claude-code-reference.md §2, verified row-by-row against
// testdata/reference/claude-code/mode-cycle.txt): auto -> manual ->
// acceptEdits -> plan -> auto. bypassPermissions and dontAsk are not in the
// ring — cycleMode sends either of them straight to auto, matching the
// task's explicit instruction, since neither is ever entered by cycling.
var permissionModeRing = []string{"auto", "manual", "acceptEdits", "plan"}

// ResolveMentions is the app's mention-resolution hook. tui cannot import
// internal/cli (cli is going to import tui, and Go forbids the cycle), so
// mention resolution — which lives in internal/cli/mentions.go — is
// injected as a function instead of imported directly.
type ResolveMentionsFunc func(ctx context.Context, line string) (prompt string, images []msg.ImageContent, describeLines []string)

// RunPromptHooksFunc runs UserPromptSubmit (and, on the first call only,
// folds in SessionStart's own context — that bookkeeping lives with the
// caller in internal/cli/tui.go, matching print mode's own hook wiring)
// and reports whether the turn is blocked.
type RunPromptHooksFunc func(ctx context.Context, line string) (blockedReason string, hookContext []string)

// GitStatusFunc reads git status with its own timeout; returns ok=false
// when there is nothing to report (not a repo, or the read failed/timed
// out).
type GitStatusFunc func(ctx context.Context) (GitStatus, bool)

// Config configures NewModel. Every dependency the interactive shell
// needs is here, so app.go stays testable with a fake Bridge/Lane and no
// real harness, provider or filesystem underneath.
type Config struct {
	Cwd         string
	ModelLabel  string
	Tier        budget.Tier
	InitialMode string
	StartedAt   time.Time
	Plain       bool
	// Fullscreen selects kiln's alt-screen TUI: a scrolling transcript
	// viewport with the input pinned at the bottom (docs/kiln-fullscreen-plan.md).
	// Falls back to inline under Plain (screen-reader mode) — see NewModel.
	Fullscreen     bool
	StartupContext []string
	// Effort is the reasoning effort label shown in the hint row above the
	// input box (`◐ medium · /effort`, docs/claude-code-reference.md §2).
	// Empty defaults to "medium" in NewModel.
	Effort string

	Env            *execenv.Env
	Gate           *permission.Gate
	PlanController *agent.PlanController
	Lane           *harness.Lane
	Registry       *commands.Registry
	Bridge         *Bridge

	ResolveMentions ResolveMentionsFunc
	RunPromptHooks  RunPromptHooksFunc
	GitStatus       GitStatusFunc

	// HistoryPath, if set, is appended to (editor.Append) after every
	// submitted line.
	HistoryPath string
	// StartupHistory seeds the editor's in-memory history (Up/Down
	// recall) at startup, matching app.ts's loadHistory().then(...)
	// seeding — without it, Up arrow on a fresh editor has nothing to
	// walk.
	StartupHistory []string
	// SessionName sets the terminal title via View.WindowTitle.
	SessionName string
	// Keymap is the editor's resolved key bindings (defaults plus the
	// user's ~/.claude/keybindings.json overrides); nil keeps defaults.
	Keymap editor.Keymap
	// Banner is the startup banner, committed (fitted to the terminal
	// width) on the first WindowSizeMsg so a long cwd row never wraps.
	Banner []string
	// BannerFunc, when set, re-renders Banner at commit time, so the rows
	// use the text tokens fitted to the terminal's reported background
	// rather than the design defaults Banner was styled with at startup.
	BannerFunc func() []string
	// ModelID is the provider/model id the verbose transcript's model row
	// shows after a turn's last tool call (docs/claude-code-reference.md
	// §3); empty falls back to ModelLabel.
	ModelID string
	// NeedsTrust opens the folder-trust dialog before the first prompt
	// (docs/claude-code-reference.md §5, dialog-trust.txt). OnTrust receives
	// the answer; "No, exit" quits the program.
	NeedsTrust     bool
	OnTrust        func(trusted bool)
	SessionID      string
	TranscriptPath string
	Version        string
	// IsResume is set when this run resumed an existing session
	// (--resume/--continue): NewModel's first WindowSizeMsg handler
	// replays Lane's prior entries into the transcript, right after the
	// banner, exactly once — the same rendering replayTranscript uses for
	// Ctrl+O/resize, so a resumed session never opens on an empty
	// transcript (defect *resumed-session-no-transcript-replay).
	IsResume bool
}

// Model is the interactive shell's Bubbletea v2 model — the Go port of
// app.ts's runApp, restructured around Update/View instead of runApp's
// direct pi-tui component tree. See doc.go for the mapping.
type Model struct {
	cfg Config

	width, height int

	editor  editor.Model
	spinner SpinnerState
	footer  *FooterState
	prompt  *PromptState
	// subagents tracks the current turn's `task` dispatches for the
	// subagents panel (subagents.go); reset at the start of every turn in
	// beginTurn.
	subagents *SubagentPanelState
	// plan is the live "plan" block's state (plan.go's RenderPlan),
	// driven by MsgTodos on every todo_write call; empty when no plan is
	// active. Reset at the start of every turn in beginTurn, same as
	// subagents, and frozen (committed) once in its final state — either
	// by finishTurn, or earlier, the moment anything else commits while it
	// is still live (see live_freeze.go). A pointer, like subagents,
	// shared across every Model value: Bridge's freeze hook
	// (NewModel's SetFreezeHook call) reads and freezes it from whichever
	// goroutine is committing, not just Update's.
	plan *planLiveState
	// liveWidth mirrors contentWidth() for the freeze hook, which runs on
	// whatever goroutine calls a commit method — not necessarily Update's,
	// which is the only place m.width itself is safe to read. Updated on
	// every WindowSizeMsg.
	liveWidth *atomic.Int32
	// retry is the live "error" retry block's state (retry.go), non-nil
	// while a retry is pending after a retryable stream failure; cleared on
	// MsgRetryStart (the delayed attempt is starting) or at turn end.
	retry *RetryView
	// reconnectedAttempt is set by MsgRetryStart (1-based attempt about to
	// run) and consumed by the next thing that would otherwise commit
	// first — a streamed message's first delta or its EventMessageEnd
	// commit — which commits "↺ Reconnected on attempt N" just ahead of it.
	// Zero means no reconnect note is owed.
	reconnectedAttempt int
	// streamText is the assistant text streamed so far in the live region
	// (stream.go's RenderStreamLive), cleared once the full markdown block
	// commits (msgCommitMarkdown) or the turn ends.
	streamText string
	dialog     Dialog
	thinking   *ThinkingView
	// queued holds the raw text of every follow-up submitted via Lane.Steer
	// while a turn is busy, in submission order, that the lane has not yet
	// drained onto the branch (harness/turn.go's drainInbox). Rendered in
	// the live region (liveTail) as a dim "queued" row rather than
	// committed to the transcript — committing it early is defect
	// 20260926T232249Z-queued-block-order: it lands above the reply to the
	// turn it interrupted, and its "queued" meta never clears. Once the
	// lane actually drains the inbox, MsgQueue{Len:0} (EventQueueUpdate)
	// commits every pending item here as an ordinary `you` block, in
	// order, and clears this slice — which lands it after the reply it
	// followed, since the drain always happens after that reply's own
	// commit (turn.go's drive loop: text commits via EventMessageEnd/
	// msgCommitMarkdown, then EventTurnEnd, then drainInbox). An aborted
	// turn (Esc) never drains — those items stay here, still visible in
	// the live region, until a later Prompt call's own drain (Lane.Prompt
	// drains the inbox before its own entry is parented) delivers them.
	queued []string
	// popup is the `/` or `@` autocomplete list, non-nil while one of the
	// two triggers matches the editor's current line/cursor. Rebuilt from
	// scratch on every keystroke by refreshPopup — see autocomplete.go.
	popup *Popup
	// shortcuts is set while the `?` shortcuts panel is shown under the
	// input box (docs/claude-code-reference.md §6); any key closes it.
	shortcuts bool
	// bannerDone is set once Banner has been committed.
	bannerDone bool
	// bannerRowCount is how many rows commitBanner's own commit contributed
	// to committedRows (0 if there was no banner to commit). Compared
	// against committedRows by transcriptIsEmpty to tell whether anything
	// besides the banner has been committed yet — see that method's doc
	// comment.
	bannerRowCount int
	// bannerScheduled guards msgCommitBanner's grace-period tick (see
	// bannerBackgroundGrace) so the first WindowSizeMsg schedules it
	// exactly once, not once per resize before the banner has committed.
	bannerScheduled bool
	// committedRows counts rows printed above the live region since the last
	// clear (tea.PrintedLines). View pads the live region so the input box
	// sits at the bottom of the terminal; a constant-height live region is
	// also what keeps the renderer's cursor tracking stable across a
	// dialog/prompt opening and closing (a shrinking live region desyncs it
	// and the cursor lands above the box).
	committedRows int
	// dialogEcho is the slash command line whose dialog is open; Claude
	// Code echoes the command (and its result row) only once the dialog
	// closes (docs/claude-code-reference.md §3: "❯ /model" / "  ⎿  Kept
	// model as …").
	dialogEcho string
	// group is the in-flight collapsed row for consecutive read-only tool
	// calls ("  Reading 2 files…", docs/claude-code-reference.md §3). It is
	// live (redrawn every frame) until a non-grouped commit or the turn's
	// end flushes it into scrollback as "  Read 2 files".
	group *toolGroup

	busy          bool
	turn          int
	turnStartedAt time.Time

	quitting bool

	// justKilled is true for exactly the one frame right after a Ctrl+K or
	// Ctrl+U kill, and false again from the very next keystroke on — it
	// drives the "Ctrl+Y to paste deleted text" hint replacing the effort
	// indicator above the input box (docs/claude-code-reference.md §2,
	// mode-manual.txt row 8: "until the next keystroke").
	justKilled bool

	// modeHintText/modeHintGen replace the mode line for one second after a
	// Ctrl+C ("Press Ctrl-C again to exit", ctrl-c-hint.txt). modeHintGen
	// guards the delayed msgClearModeHint the Hint action schedules: a
	// second Ctrl+C's own 1s timer must not be cancelled early by the
	// first's, so the clear message only takes effect if the generation it
	// carries still matches the one currently showing.
	modeHintText string
	modeHintGen  int

	// lastCtrlC/lastEsc persist Router's double-press timing across
	// keypresses. handleKey rebuilds a fresh Router on every call (so its
	// action closures always close over the *current* m, not a stale one
	// from an earlier Update — see handleKey's own comment), which would
	// otherwise reset this timing to zero on every single key and make a
	// second Ctrl+C/Esc within the window indistinguishable from a first
	// one. Router's fields are unexported but same-package, so handleKey
	// seeds and reads them back directly instead of going through
	// NewRouter alone.
	lastCtrlC time.Time
	lastEsc   time.Time

	// startupContext is consumed by the first turn, then emptied, matching
	// app.ts's own mutable startupContext.
	startupContext []string

	// initCmd is the editor's focus Cmd (cursor blink, etc.), captured at
	// construction and replayed by Init. Init cannot mutate the model —
	// it only returns a Cmd — so the actual editor.Focus() call has to
	// happen here, in NewModel, where its effect on m.editor is kept;
	// calling it inside Init would focus a throwaway copy of the model
	// and leave the real one permanently unfocused (every keystroke would
	// then have nowhere to go).
	initCmd tea.Cmd

	// --- fullscreen mode (docs/kiln-fullscreen-plan.md) --------------------

	// fullscreen is cfg.Fullscreen && !cfg.Plain, computed once in NewModel
	// and flipped at runtime by toggleFullscreen (ctrl+f). Plain mode never
	// enters fullscreen: it has no alt-screen rendering to fall back from.
	fullscreen bool
	// transcript holds every committed row while fullscreen (already
	// rendered, one string per terminal row), rebuilt from the session log
	// on ClearScreen/toggle/resize-rewrap — see appendTranscript and
	// replayTranscript.
	transcript []string
	// sel is the fullscreen transcript selection being dragged or last
	// copied (selection.go); nil when there is none.
	sel *selection
	// viewport renders transcript, scrolled. Its own KeyMap is emptied in
	// NewModel (see there) so it never intercepts a keypress on its own;
	// scrolling is driven explicitly from handleKey/Update instead.
	viewport viewport.Model
	// resizeGen guards the debounced re-wrap a width change schedules
	// (msgResizeRewrap): only the most recent WindowSizeMsg's tick may
	// trigger the clear+replay, so a burst of resizes during a drag
	// rewraps once, not once per event.
	resizeGen int
}

// editorStyles builds the input box's editor.Styles from the theme
// package's *current* adaptive tokens (CurrentTextHex/CurrentSurfaceHex) —
// never the design's fixed hexAmber/hexRuleStrong/hexFaint consts directly,
// or a rebuild after SetTerminalBackground adjusts those tokens for a
// non-design terminal background would just reapply the same stale dark
// colours (defect *light-bg-you-text-invisible). Called once in NewModel
// (before any background reply, so it still reads the design defaults
// resetTextTokensToDesign/resetSurfaceTokensToDesign seeded at init) and
// again from the tea.BackgroundColorMsg handler, once SetTerminalBackground
// has recomputed them for the real terminal.
func editorStyles(marker string) editor.Styles {
	text, surface := CurrentTextHex(), CurrentSurfaceHex()
	return editor.Styles{
		Marker: marker,
		// Kiln: prompt glyph amber, rules in rule-strong, placeholder in
		// kiln faint — see theme.go's hexAmber/hexRuleStrong/hexFaint doc
		// comments for what those tokens mean on the design background;
		// CurrentTextHex/CurrentSurfaceHex give whatever they currently
		// resolve to for the detected terminal background.
		MarkerStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(text.Amber)),
		Rule:        lipgloss.NewStyle().Foreground(lipgloss.Color(surface.RuleStrong)),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color(text.Faint)),
		RuleChar:    RuleFillChar(),
	}
}

// abbrevHomeEnv home-abbreviates path against os.UserHomeDir(), falling
// back to path unchanged when the home directory cannot be read — the
// status line's location segment (status.go RenderStatusLine, AbbrevHome).
func abbrevHomeEnv(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return AbbrevHome(path, home)
}

// NewModel builds the interactive shell's model, already focused. Init
// still needs to be called (the standard Bubbletea contract) so its
// returned Cmd — the editor's cursor-blink command — actually runs.
func NewModel(cfg Config) Model {
	// HARNESS_TEST_CLOCK, when set, overrides StartedAt too — a PTY golden
	// asserting the elapsed clock needs both the "now" the footer renders
	// with (FooterState.Render already applies the override) and the
	// "session start" it is measured against to come from the same frozen
	// clock, or the elapsed figure is real wall-clock skew dressed up as a
	// deterministic one.
	if t, ok := clockOverride(); ok {
		cfg.StartedAt = t
	}
	if cfg.Effort == "" {
		cfg.Effort = "medium"
	}
	marker := G().UserMark
	ed := editor.New(editorStyles(marker))
	m := Model{
		cfg:            cfg,
		editor:         ed,
		footer:         NewFooterState(StatusState{ModelLabel: cfg.ModelLabel, ContextWindow: cfg.Tier.ContextWindow, Mode: cfg.InitialMode, StartedAt: cfg.StartedAt, Cwd: abbrevHomeEnv(cfg.Cwd)}),
		prompt:         NewPromptState(cfg.Cwd),
		subagents:      NewSubagentPanelState(),
		plan:           &planLiveState{},
		liveWidth:      &atomic.Int32{},
		startupContext: append([]string(nil), cfg.StartupContext...),
		fullscreen:     cfg.Fullscreen && !cfg.Plain,
		viewport:       viewport.New(),
	}
	if cfg.Bridge != nil {
		liveWidth := m.liveWidth
		cfg.Bridge.SetFreezeHook(newFreezeHook(m.plan, m.subagents, func() int { return int(liveWidth.Load()) }))
	}
	// The viewport must never handle a key or wheel event on its own: every
	// scroll it makes has to go through handleKey/Update explicitly (PgUp/
	// PgDn/Shift+Up/Down here, wheel in Update), or a key meant for the
	// editor (plain Up/Down, j/k while typing) would be silently eaten by
	// viewport.Update instead of reaching the editor. An empty KeyMap means
	// no binding ever matches; MouseWheelEnabled=false means viewport.Update
	// ignores wheel messages too — Update's own tea.MouseWheelMsg case
	// drives ScrollUp/ScrollDown directly instead.
	m.viewport.KeyMap = viewport.KeyMap{}
	m.viewport.MouseWheelEnabled = false
	if len(cfg.StartupHistory) > 0 {
		m.editor.SetHistory(cfg.StartupHistory)
	}
	if cfg.Keymap != nil {
		m.editor.SetKeymap(cfg.Keymap)
	}
	if cfg.NeedsTrust {
		onTrust, bridge := cfg.OnTrust, cfg.Bridge
		m.dialog = NewTrustDialog(cfg.Cwd, func(trusted bool) {
			if onTrust != nil {
				onTrust(trusted)
			}
			// Off the Update goroutine: Send blocks on the event loop.
			if bridge != nil {
				go bridge.Send(MsgTrustAnswered{Trusted: trusted})
			}
		})
	}
	m.initCmd = m.editor.Focus()
	return m
}

func (m Model) Init() tea.Cmd {
	// Ask the terminal for its background color so SetTerminalBackground can
	// make the hairline/tint surface tokens visible against it instead of
	// assuming the design's own #14110d (theme.go's doc comment on Rule).
	// tea.RequestBackgroundColor's OSC 11 query is answered as a
	// tea.BackgroundColorMsg (handled in update below) — never, on a
	// terminal that doesn't support it, in which case the design's tokens
	// stand as they always have (theme.go's SetTerminalBackground is simply
	// never called, no 300ms timer needed to "give up" and fall back).
	return tea.Batch(m.initCmd, tea.RequestBackgroundColor)
}

// --- messages owned by app.go itself ------------------------------------

type msgSpinnerTick struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return msgSpinnerTick{} })
}

// msgClearModeHint restores the mode line one second after a Ctrl+C
// replaced it with "Press Ctrl-C again to exit" (see Model.modeHintGen's
// doc comment).
type msgClearModeHint struct{ gen int }

// modeHintDuration is how long the Ctrl+C hint replaces the mode line,
// matching keys.ts's DOUBLE_PRESS_MS window that the hint is meant to
// describe.
const modeHintDuration = time.Second

// toolGroup accumulates consecutive grouped read-only tool calls of one
// kind: the live region still shows one collapsed "Reading N files…" row
// (RenderToolGroupRunning, keyed off len(views)) while they are in flight,
// but each call's own view is kept so flushGroup can commit a full tool
// block per call once the group ends — read-only calls no longer collapse
// to "  Read N files" in the committed transcript (docs/kiln-design-
// handoff/README.md's "tool" row applies to every call, not just
// mutating ones).
type toolGroup struct {
	kind  GroupKind
	views []ToolCallView
}

// msgResizeRewrap follows a debounced width change in fullscreen mode:
// once 150ms have passed with no further WindowSizeMsg, the transcript is
// cleared and replayed at the new width (the same tea.ClearScreen +
// msgReplayTranscript sequence Ctrl+O uses), so history re-wraps instead of
// staying wrapped to a stale width. gen must match Model.resizeGen at the
// time the tick fires, or a later resize already superseded this one.
type msgResizeRewrap struct{ gen int }

// msgReplayTranscript follows the tea.ClearScreen a Ctrl+O toggle returns:
// once the clear has been applied, the transcript so far is re-committed at
// the new verbosity (see replayTranscript and Bridge.MsgClearAndReplay).
type msgReplayTranscript struct {
	// then runs after the replay has been queued: a note that must land
	// under the redrawn history. A tea.Sequence step after this message
	// runs as soon as the message is returned, before Update replays, and
	// so committed its note above the redrawn transcript.
	then func()
}

// msgExecDone reports that a program a command handed the terminal to (the
// editor /memory opens) has exited, with the note to show for it.
type msgExecDone struct{ note string }

// bannerBackgroundGrace is how long the first WindowSizeMsg's handler waits
// for tea.BackgroundColorMsg before committing the banner (and, for a
// resumed session, replaying the transcript) with whatever theme tokens are
// live at that point — see the WindowSizeMsg case's own comment for why
// committing inline there instead always loses that race. Local PTY round
// trips land in low single-digit milliseconds; a terminal that never
// answers the OSC 11 query still gets its banner once this fires.
const bannerBackgroundGrace = 30 * time.Millisecond

// msgCommitBanner fires bannerBackgroundGrace after the first WindowSizeMsg,
// unless tea.BackgroundColorMsg's own handler already committed the banner
// first — see commitBanner, which either call reaches, guarded by
// bannerDone so it never double-commits.
type msgCommitBanner struct{}

// MsgTrustAnswered carries the trust dialog's answer; "No, exit" quits.
type MsgTrustAnswered struct{ Trusted bool }

// msgTurnResult carries what a turn cost, once lane.Prompt returns, so
// Update can commit the turn summary/error and clear busy state. Built by
// the goroutine app.go's handleSubmit spawns, not by the bridge, since
// nothing about it is harness-event-sourced.
type msgTurnResult struct {
	result  harness.RunResult
	err     error
	seconds int
}

// --- Update ---------------------------------------------------------------

// Update wraps update with the one thing every fullscreen message handler
// needs done afterward: the viewport resized to whatever room is left below
// the (possibly just-changed) bottom region. Doing it here, once, means
// update's own cases never have to remember to call layoutViewport
// themselves.
func (m Model) Update(tm tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(tm)
	nm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	if nm.fullscreen {
		nm = nm.layoutViewport()
	}
	return nm, cmd
}

func (m Model) update(tm tea.Msg) (tea.Model, tea.Cmd) {
	if body, ok := tea.PrintedLines(tm); ok {
		m.committedRows += strings.Count(body, "\n") + 1
		return m, nil
	}
	if tea.IsClearScreen(tm) {
		m.committedRows = 0
		if m.fullscreen {
			m.transcript = nil
			m.viewport.SetContent("")
			m.viewport.GotoTop()
		}
		return m, nil
	}
	switch msg := tm.(type) {
	case MsgTranscriptAppend:
		m = m.appendTranscript(strings.Split(msg.Text, "\n"))
		return m, nil

	case msgResizeRewrap:
		if msg.gen != m.resizeGen {
			return m, nil
		}
		return m, tea.Sequence(tea.ClearScreen, func() tea.Msg { return msgReplayTranscript{} })

	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		next, cmd, _ := m.handleMouseSelect(msg)
		return next, cmd

	case msgCopied:
		m.modeHintGen++
		gen := m.modeHintGen
		m.modeHintText = copiedNote(msg)
		return m, tea.Tick(copiedNoteDuration, func(time.Time) tea.Msg { return msgClearModeHint{gen: gen} })

	case tea.MouseWheelMsg:
		if !m.fullscreen {
			return m, nil
		}
		switch msg.Button {
		case tea.MouseWheelUp:
			m.viewport.ScrollUp(3)
		case tea.MouseWheelDown:
			m.viewport.ScrollDown(3)
		}
		return m, nil

	case tea.WindowSizeMsg:
		prevWidth := m.width
		m.width, m.height = msg.Width, msg.Height
		if m.liveWidth != nil {
			m.liveWidth.Store(int32(m.contentWidth()))
		}
		m.editor.SetWidth(m.liveEditorWidth())
		// Kiln label rules and full-row tints size to the live content
		// width (transcript.go's width-less Render* helpers read this).
		// SetRenderMargin feeds Bridge.Commit's own central margin pad
		// (bridge.go), which runs off arbitrary goroutines the same way
		// ruleWidth()'s renderWidth does.
		SetRenderWidth(m.contentWidth())
		SetRenderMargin(m.margin())
		var bannerCmd tea.Cmd
		if !m.bannerDone && !m.bannerScheduled {
			// The banner (and, for a resumed session, the transcript
			// replay right after it — see commitBanner) is deferred by
			// bannerBackgroundGrace rather than committed here inline:
			// bubbletea's own Run() sends this WindowSizeMsg (tty.go,
			// `go p.Send(resizeMsg)`) before Init()'s Cmd even starts
			// executing, so tea.RequestBackgroundColor's OSC 11 query (sent
			// from that Cmd) can never win this race — committing straight
			// from this handler always bakes in the pre-detection
			// dark-design hexRuleStrong for the banner's closing rule,
			// wrong on a light terminal (confirmed against qa/runs/c3/
			// iterm-light/content-verbose-toggle-120x40/02-reply-non-
			// verbose.png: that rule's pixels are RGB(41,37,29), the
			// unadjusted design token, while every rule committed after
			// SetTerminalBackground runs reads RGB(212,210,203) instead —
			// defect *light-bg-you-text-invisible). The grace period gives
			// a real terminal's OSC 11 round trip a chance to land first
			// (tea.BackgroundColorMsg's own handler below commits
			// immediately and cancels this tick's effect via bannerDone);
			// a terminal that never answers still gets its banner once the
			// tick fires — no indefinite wait, matching Init's own "no
			// timer needed to give up" default for everything else.
			m.bannerScheduled = true
			bannerCmd = tea.Tick(bannerBackgroundGrace, func(time.Time) tea.Msg { return msgCommitBanner{} })
		}
		if m.fullscreen {
			m = m.layoutViewport()
		}
		// A width change redraws the transcript at the new width once the
		// resize settles. Inline mode needs it as much as fullscreen: a
		// frame painted at the old width between the terminal's resize and
		// this message wraps, the renderer loses count of the rows it owns,
		// and pieces of the old frame stay on screen.
		if prevWidth != 0 && prevWidth != m.width && (m.fullscreen || m.bannerDone) {
			m.resizeGen++
			gen := m.resizeGen
			return m, tea.Batch(bannerCmd, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return msgResizeRewrap{gen: gen} }))
		}
		return m, bannerCmd

	case tea.QuitMsg:
		m.quitting = true
		if m.cfg.Bridge != nil {
			m.cfg.Bridge.Stop()
		}
		return m, tea.Quit

	case tea.KeyPressMsg:
		m.sel = nil
		return m.handleKey(msg)

	case tea.PasteMsg:
		// Terminals deliver a paste as one bracketed block, not keys; it
		// goes to whatever text field is active: a permission prompt's
		// reason, else the input. Dialogs have no text fields.
		m.shortcuts = false
		if m.dialog != nil {
			return m, nil
		}
		if m.prompt.Active() {
			m.prompt.Paste(msg.Content)
			return m, nil
		}
		ed, cmd, _ := m.editor.Update(msg)
		m.editor = ed
		m = m.refreshPopup()
		return m, cmd

	case msgSpinnerTick:
		if !m.busy {
			return m, nil
		}
		m.spinner.Tick()
		return m, tickCmd()

	case msgCommitMarkdown:
		m = m.flushGroup()
		m.streamText = ""
		if m.cfg.Bridge != nil {
			m = m.commitReconnectNote()
			renderer := NewMarkdownRenderer(m.contentWidth(), IsPlain())
			lines := append([]string{""}, RenderAssistantText(renderer.Render(msg.Text))...)
			m.commit(lines)
		}
		return m, nil

	case MsgStreamText:
		if IsPlain() {
			// A screen reader would re-read the live region every tick;
			// the streaming caret never renders in plain mode (D's spec).
			return m, nil
		}
		m = m.commitReconnectNote()
		// Text after a held read group ends the group: commit it now, or
		// the calls stay off screen while the reply that follows them
		// streams in.
		// The reply streaming is also when "Running <tool>" stops being
		// true (the design's busy label returns to the turn's gerund once
		// text resumes).
		if m.streamText == "" {
			if m.group != nil {
				m = m.flushGroup()
			}
			m.spinner.ResetLabel()
		}
		m.streamText = msg.Text
		return m, nil

	case MsgRetry:
		until := time.Now().Add(msg.Delay)
		if t, ok := clockOverride(); ok {
			until = t.Add(msg.Delay)
		}
		m.retry = &RetryView{Message: msg.Message, Attempt: msg.Attempt, Max: msg.MaxAttempts, Until: until}
		return m, nil

	case MsgQueue:
		m.spinner.SetQueueLen(msg.Len)
		// Len 0 is only ever sent by the lane's own drain (drainInbox's
		// EventQueueUpdate, turn.go) — Steer's own EventQueueUpdate always
		// carries the inbox's new, non-zero length. A drain means every
		// pending item just got re-parented onto the branch, in the same
		// order they were queued, so commit them here, in that order, as
		// plain `you` blocks (no "queued" meta — it already delivered).
		if msg.Len == 0 && len(m.queued) > 0 {
			width := m.contentWidth()
			for _, text := range m.queued {
				m.commit(RenderUserMessage(text, width))
			}
			m.queued = nil
		}
		return m, nil

	case MsgRetryStart:
		// ev.Attempt (harness/turn.go's EventRetryStart) is already the
		// 1-based attempt about to run ("attempt+1" at the emit site), so
		// it is exactly the N the reconnect note reports — no further
		// adjustment.
		m.retry = nil
		m.reconnectedAttempt = msg.Attempt
		return m, nil

	case msgCommitFault:
		// A bash/read call that ran before the fault may still sit in the
		// collapsed group, uncommitted; flush it first, as msgCommitToolCall
		// does for any non-grouped block.
		m = m.flushGroup()
		m.commit(RenderError(msg.Message))
		return m, nil

	case msgCommitToolCall:
		if m.cfg.Bridge == nil {
			return m, nil
		}
		// A dispatch the subagents panel shows (its row carries the
		// outcome, success or failure) gets no task block of its own: the
		// design's agents block is the one record of a dispatch. A task
		// call that failed before dispatching has no row and still shows.
		if strings.EqualFold(msg.View.Name, "task") && m.subagents.Has(msg.CallID) && !m.cfg.Bridge.Verbose() {
			return m, nil
		}
		if kind, grouped := groupKindFor(msg.View.Name); grouped && !m.cfg.Bridge.Verbose() {
			if m.group != nil && m.group.kind != kind {
				m = m.flushGroup()
			}
			if m.group == nil {
				m.group = &toolGroup{kind: kind}
			}
			m.group.views = append(m.group.views, msg.View)
			return m, nil
		}
		m = m.flushGroup()
		view := msg.View
		lines := append([]string{""}, FitLines(RenderToolCall(view), m.contentWidth(), "     ")...)
		m.commit(lines)
		return m, nil

	case MsgRefreshMode:
		return m.refreshMode(), nil

	case MsgSubagentEvent:
		m.subagents.Apply(msg.Event)
		return m, nil

	case MsgTodos:
		m.plan.Set(msg.Items)
		return m, nil

	case MsgFooterNote:
		m.footer.SetNote(msg.Text)
		return m, nil

	case msgDialogResult:
		if applier, ok := m.dialog.(interface{ Apply(msgDialogResult) }); ok {
			applier.Apply(msg)
			// A dialog whose choice is final (a model picked) closes once
			// it has applied, leaving its outcome as the confirmation.
			if d, ok := m.dialog.(interface{ Done() bool }); ok && d.Done() {
				m = m.closeDialog()
			}
			// A dialog action can move the gate's mode (/permissions' "m");
			// the footer re-reads it so it agrees once the panel closes.
			return m.refreshMode(), nil
		}
		// The dialog already closed (Rewind closes on Enter): its outcome
		// is a note in the transcript, after the redrawn history when the
		// dialog changed what that history is.
		note := func() tea.Msg {
			if m.cfg.Bridge == nil {
				return nil
			}
			if msg.err != nil {
				m.cfg.Bridge.Commit(RenderError(msg.err.Error()))
			} else if msg.msg != "" {
				m.cfg.Bridge.CommitNote(msg.msg)
			}
			return nil
		}
		if msg.replay {
			return m, tea.Sequence(tea.ClearScreen, func() tea.Msg { return msgReplayTranscript{then: func() { note() }} })
		}
		return m, note

	case msgReplayTranscript:
		m.replayTranscript()
		if msg.then != nil {
			msg.then()
		}
		return m, nil

	case msgExecDone:
		if msg.note != "" {
			m.commitNote(msg.note)
		}
		return m, nil

	case msgCommitBanner:
		return m.commitBanner(), nil

	case MsgTrustAnswered:
		if !msg.Trusted {
			return m, tea.Quit
		}
		return m, nil

	case MsgClearAndReplay:
		return m, tea.Sequence(tea.ClearScreen, func() tea.Msg { return msgReplayTranscript{} })

	case MsgThinking:
		return m.handleThinking(msg), nil

	case MsgTokens:
		m.spinner.SetTokens(msg.Tokens)
		return m, nil

	case MsgUsage:
		if msg.Tokens != nil {
			m.spinner.SetTokens(*msg.Tokens)
		}
		cost := m.footer.State().Cost
		if msg.Cost != nil {
			cost = *msg.Cost
		}
		m.footer.Apply(StatusPatch{ContextUsed: msg.ContextUsed, Cost: &cost})
		return m, nil

	case MsgModelInfo:
		label := msg.Label
		window := msg.ContextWindow
		m.footer.Apply(StatusPatch{ModelLabel: &label, ContextWindow: nonZeroOr(window, m.footer.State().ContextWindow)})
		var nilInt *int
		m.footer.Apply(StatusPatch{ContextUsed: nilInt})
		return m, nil

	case MsgGitStatus:
		g := msg.Status
		m.footer.Apply(StatusPatch{Git: &g})
		return m, nil

	case tea.BackgroundColorMsg:
		SetTerminalBackground(msg.Color)
		// The banner divider and every other Rule/RuleStrong/Muted/...
		// call site re-reads the theme package's adaptive tokens on its
		// next render, but the editor's own Styles were captured by value
		// in NewModel (marker/rule/placeholder colours, plus the cursor
		// colour neutralTextareaStyles derives from MarkerStyle) — they
		// need pushing back in explicitly or the input box stays on the
		// pre-detection dark-design hexes forever (defect
		// *light-bg-you-text-invisible).
		m.editor.SetStyles(editorStyles(G().UserMark))
		if m.bannerScheduled && !m.bannerDone {
			// The background reply beat msgCommitBanner's grace-period
			// tick: commit the banner now, with the just-adapted tokens,
			// instead of waiting out the rest of bannerBackgroundGrace for
			// no reason. bannerScheduled guards this on the first
			// WindowSizeMsg having already run (m.width/height known); if
			// it somehow hasn't yet, the later tick still commits once it
			// does.
			m = m.commitBanner()
		}
		return m, nil

	case MsgPermissionPrompt:
		m = m.flushGroup()
		// The design's "approval needed" block stands alone: no pre-prompt
		// tool header commits above it (docs/kiln-design-handoff/README.md
		// "Interactions"). The tool's own block commits after the decision,
		// on EventToolEnd, exactly like any other call — with the outcome
		// (approved/auto-approved) in its meta.
		p := m.prompt
		p.pending = &pendingPermission{request: msg.Request, reply: msg.Reply}
		p.feedback = nil
		p.plan = nil
		m.spinner.SetLabel("Waiting for approval")
		return m.syncPromptPlaceholder(), nil

	case MsgPlanPrompt:
		p := m.prompt
		p.plan = &pendingPlan{plan: msg.Plan, reply: msg.Reply}
		p.feedback = nil
		p.pending = nil
		m.spinner.SetLabel("Waiting for approval")
		return m.syncPromptPlaceholder(), nil

	case MsgSpinnerLabel:
		m.spinner.SetLabel(msg.Text)
		return m, nil

	case MsgSpinnerReset:
		m.spinner.ResetLabel()
		return m, nil

	case msgTurnResult:
		return m.finishTurn(msg), nil

	case msgClearModeHint:
		if msg.gen == m.modeHintGen {
			m.modeHintText = ""
		}
		return m, nil
	}

	return m, nil
}

func nonZeroOr(v, fallback int) *int {
	if v != 0 {
		return &v
	}
	return &fallback
}

func (m Model) handleThinking(msg MsgThinking) Model {
	switch {
	case msg.Ended:
		if m.thinking != nil && strings.TrimSpace(m.thinking.Text) != "" {
			view := *m.thinking
			view.Active = false
			if m.cfg.Bridge != nil {
				view.Expanded = m.cfg.Bridge.Verbose()
			}
			width := m.contentWidth()
			lines := append([]string{""}, FitLines(RenderThinking(view), width, resultIndent)...)
			m.commit(lines)
		}
		m.thinking = nil
		m.spinner.ResetLabel()
		thinking := false
		m.footer.Apply(StatusPatch{Thinking: &thinking})
		return m
	case m.thinking == nil:
		m.thinking = &ThinkingView{Active: true}
		m.spinner.SetLabel("Thinking")
		thinking := true
		m.footer.Apply(StatusPatch{Thinking: &thinking})
		return m
	default:
		m.thinking.Text += msg.Text
		return m
	}
}

func (m Model) finishTurn(msg msgTurnResult) Model {
	m = m.flushGroup()
	m.busy = false
	m.spinner.Stop()
	m.footer.SetBusy(false)
	// Any live retry countdown and in-flight streaming text are dropped,
	// not committed, on every path through finishTurn (B's spec): a
	// completed/failed turn already replaces them with its own commit
	// (the assistant's text, or RenderError below); an aborted one commits
	// the interrupted-tool/plan/subagents/note sequence instead, with no
	// role for either live block.
	m.retry = nil
	m.streamText = ""
	m.reconnectedAttempt = 0

	if msg.result.Status == harness.StatusAborted {
		// Esc while busy (B's spec): each tool call that started but never
		// reached EventToolEnd commits as a red CallError block, then the
		// live plan/subagents panels commit their final state (already
		// below), then the "■ Interrupted" note — in that order, so the
		// note reads as the last thing that happened.
		if m.cfg.Bridge != nil {
			width := m.contentWidth()
			for _, view := range m.cfg.Bridge.InFlightTools() {
				m.cfg.Bridge.Commit(append([]string{""}, FitLines(RenderToolCall(view), width, "     ")...))
			}
		}
	} else if msg.err != nil {
		if m.cfg.Bridge == nil || !m.cfg.Bridge.FaultCommitted(msg.err.Error()) {
			m.commit(RenderError(msg.err.Error()))
		}
	} else if msg.result.Status != harness.StatusCompleted {
		m.commit(RenderError(msg.result.Status))
	}

	// The verbose "HH:MM AM model" row that used to commit here (before
	// the plan/subagents freeze below) is gone: the kiln design's "text"
	// block has no meta column at all (docs/kiln-design-handoff/README.md
	// "Block anatomy" table — "kiln (dim) | – |"), so there is nowhere on
	// a label rule for it to sit, and as its own row it was defect
	// 20260926T232657Z-verbose-toggle-inconsistent's stray "7:00 PM
	// faux/faux-1" line — dropped per that defect's own "or is dropped if
	// the design has no place for it".
	width := m.contentWidth()
	// The live "plan" checklist and subagents panel both commit their
	// final state once, as ordinary transcript blocks, instead of just
	// vanishing from the live region when the turn ends (docs/kiln-design-
	// handoff/README.md: "Blocks update in place" — the last update is the
	// one that has to survive into scrollback). freeze is idempotent: if
	// the "live while last" rule (live_freeze.go) already froze either one
	// mid-turn (because something else committed after it), this is a
	// no-op for that one.
	if lines := m.plan.freeze(width); len(lines) > 0 && m.cfg.Bridge != nil {
		m.cfg.Bridge.CommitSynthetic(lines)
	}
	m.plan.Reset()
	if lines := m.subagents.FreezeFinal(width); len(lines) > 0 && m.cfg.Bridge != nil {
		m.cfg.Bridge.CommitSynthetic(lines)
	}
	m.subagents.Reset()
	if msg.result.Status == harness.StatusAborted && m.cfg.Bridge != nil {
		// Follow-ups queued during the interrupted turn go back into the
		// input rather than riding along with whatever the user says
		// next: the interrupt usually means the plan changed. m.queued is
		// cleared first, so ClearInbox's MsgQueue{Len:0} commits nothing.
		note := "■ Interrupted. Tell kiln what to do instead."
		if len(m.queued) > 0 && m.cfg.Lane != nil {
			restored := strings.Join(m.queued, "\n")
			m.queued = nil
			m.spinner.SetQueueLen(0)
			if _, err := m.cfg.Lane.ClearInbox(); err != nil {
				m.commit(RenderError(err.Error()))
			}
			if typed := m.editor.Value(); typed != "" {
				restored += "\n" + typed
			}
			m.editor.SetValue(restored)
			note = "■ Interrupted. Your queued message is back in the input: edit it or press enter to send."
		}
		// Last, so it reads as the final word on what happened
		// (docs/kiln-design-handoff/README.md's "note" row).
		m.cfg.Bridge.CommitNote(note)
	}
	m.footer.SetNote("")
	m.editor.SetPlaceholder(editor.DefaultPlaceholder)
	return m.refreshMode()
}

// refreshMode re-reads the gate's permission mode into the footer, as
// app.ts's refreshStatus does after a plan approval and at turn end.
func (m Model) refreshMode() Model {
	if m.cfg.Gate == nil {
		return m
	}
	mode := string(m.cfg.Gate.Mode())
	m.footer.Apply(StatusPatch{Mode: &mode})
	return m
}

// commit freezes any live plan/subagents block ahead of lines — the "live
// while last" rule (live_freeze.go): the moment something else commits, a
// live block is frozen in place, above the newer block, and drops out of
// the live region until a later update makes it live again — then commits
// lines exactly like Bridge.Commit. Every ordinary transcript commit in
// this file goes through this (or commitSynthetic/commitNote/
// commitCommandResult below) instead of calling m.cfg.Bridge.Commit
// directly, with two deliberate exceptions: the WindowSizeMsg banner
// commit (nothing can be live yet) and finishTurn's InFlightTools abort
// loop (which must land before the plan/subagents freeze that follows it,
// not trigger it early — see live_freeze.go's doc comment).
// The left margin (layout_margin.go's padMargin) is deliberately not
// applied here: it is applied once, centrally, inside Bridge.Commit itself
// (bridge.go), which every one of these paths — and CommitSynthetic, which
// calls Commit internally — ends up going through. Padding here too would
// double it for anything that also gets stored for replay
// (CommitSynthetic's own Lines field, spliced back in by
// RenderTranscriptEntries on a Ctrl+O/Ctrl+F/Rewind redraw): the stored
// copy has to stay unpadded so a later replay, which pads the whole
// redrawn transcript once itself, doesn't pad twice.
func (m Model) commit(lines []string) {
	if m.cfg.Bridge == nil {
		return
	}
	m.cfg.Bridge.FreezeBefore()
	m.cfg.Bridge.Commit(lines)
}

// commitSynthetic is commit's CommitSynthetic counterpart.
func (m Model) commitSynthetic(lines []string) {
	if m.cfg.Bridge == nil {
		return
	}
	m.cfg.Bridge.FreezeBefore()
	m.cfg.Bridge.CommitSynthetic(lines)
}

// commitNote is commit's CommitNote counterpart.
func (m Model) commitNote(text string) {
	if m.cfg.Bridge == nil {
		return
	}
	m.cfg.Bridge.FreezeBefore()
	m.cfg.Bridge.CommitNote(text)
}

// commitCommandResult is commit's CommitCommandResult counterpart.
func (m Model) commitCommandResult(name string, lines []string) {
	if m.cfg.Bridge == nil {
		return
	}
	m.cfg.Bridge.FreezeBefore()
	m.cfg.Bridge.CommitCommandResult(name, lines)
}

// dialogOutcome is implemented by dialogs that leave a result row in the
// transcript when they close ("Kept model as …").
type dialogOutcome interface{ Outcome() string }

// closeDialog drops the open dialog and, for a slash command's dialog,
// echoes the command with its outcome row.
func (m Model) closeDialog() Model {
	outcome := ""
	if d, ok := m.dialog.(dialogOutcome); ok {
		outcome = d.Outcome()
	}
	m.dialog = nil
	if m.dialogEcho != "" && m.cfg.Bridge != nil {
		m.commit(RenderUserMessage(m.dialogEcho, m.contentWidth()))
		if outcome != "" {
			// A dialog's outcome is always one line ("Kept model as …",
			// "Compacted history · context 38% → 8%") — the kiln "note"
			// form, same as the single-line command results below.
			m.cfg.Bridge.CommitNote(outcome)
		}
	}
	m.dialogEcho = ""
	return m
}

// flushGroup commits every call the in-flight group accumulated, one full
// tool block each (RenderToolCall), once the group ends. The live region
// still showed one collapsed "Reading N files…" row while they were in
// flight (RenderToolGroupRunning, liveLines); the committed transcript
// gets the real per-call blocks instead of a collapsed summary, so a
// read-only call's status, argument and output are never lost to
// scrollback.
func (m Model) flushGroup() Model {
	if m.group == nil {
		return m
	}
	if m.cfg.Bridge != nil {
		width := m.contentWidth()
		m.cfg.Bridge.FreezeBefore()
		if CompactReadGroup(m.group.views, m.cfg.Bridge.Verbose()) {
			m.cfg.Bridge.Commit(append([]string{""}, FitLines(RenderReadGroup(m.group.views), width, "     ")...))
		} else {
			for _, view := range m.group.views {
				lines := append([]string{""}, FitLines(RenderToolCall(view), width, "     ")...)
				m.cfg.Bridge.Commit(lines)
			}
		}
	}
	m.group = nil
	return m
}

// --- fullscreen mode --------------------------------------------------------

// appendTranscript appends newLines (already-rendered rows, split on "\n")
// to the transcript buffer and re-sets the viewport's content, following
// the viewport to the bottom only if it was already there — scroll-to-pause:
// a user who scrolled up to read history is not yanked back down by new
// output arriving underneath them.
func (m Model) appendTranscript(newLines []string) Model {
	wasBottom := m.viewport.AtBottom()
	m.transcript = append(m.transcript, newLines...)
	m.viewport.SetContent(strings.Join(m.transcript, "\n"))
	if wasBottom {
		m.viewport.GotoBottom()
	}
	return m
}

// layoutViewport sizes the viewport to whatever room is left below the
// pinned bottom chrome (spinner/dialog/editor/statusline/mode line) — the
// live tail (tool-group/stream/retry/plan/subagents/prompt) is folded into
// the viewport's own content by fullscreenView, not held out of its
// height — following the viewport to the bottom if it was already there
// before the resize — the same scroll-to-pause rule appendTranscript
// applies to new content applies to a height change too, since a shrinking
// viewport can otherwise leave the offset pointing past the bottom until
// it corrects.
func (m Model) layoutViewport() Model {
	width := m.contentWidth()
	tail := m.liveTail(width)
	chrome, _, _ := m.chromeLines(width, len(tail))
	h := m.height - len(chrome)
	if h < 1 {
		h = 1
	}
	wasBottom := m.viewport.AtBottom()
	// The viewport's own width is the raw terminal width (m.width), not
	// contentWidth: every row it holds — appendTranscript's committed
	// content, which already carries the left margin from Bridge.Commit's
	// central pad (bridge.go's own doc comment), and the tail/chrome rows
	// below, also margin-padded (layout_margin.go) — is already m.width
	// columns wide by the time it reaches the viewport. Sizing the
	// viewport to the narrower contentWidth clipped exactly the margin's
	// own width back off the right edge of every row (observed: a
	// fullscreen diff header's trailing "+1  −1" truncated to "+1  −").
	rawWidth := m.width
	if rawWidth <= 0 {
		rawWidth = 80
	}
	m.viewport.SetWidth(rawWidth)
	m.viewport.SetHeight(h)
	if wasBottom {
		m.viewport.GotoBottom()
	}
	return m
}

// toggleFullscreen flips fullscreen mode (ctrl+f). A no-op in plain
// (screen-reader) mode, which has no alt-screen rendering to switch to.
// Both directions rebuild the transcript from the session log exactly like
// Ctrl+O's clear+replay — including its existing limitation: a line that
// is not a session entry (a hook notice, `!`/`#` output, a slash command's
// result row) is not in the log and is lost on toggle. That is Ctrl+O's
// contract already; toggling fullscreen inherits it rather than solving it
// separately.
func (m Model) toggleFullscreen() (tea.Model, tea.Cmd) {
	if IsPlain() {
		return m, nil
	}
	m.fullscreen = !m.fullscreen
	if m.cfg.Bridge != nil {
		m.cfg.Bridge.SetFullscreen(m.fullscreen)
	}
	m.transcript = nil
	return m, tea.Sequence(tea.ClearScreen, func() tea.Msg { return msgReplayTranscript{} })
}

// fullscreenView composes the alt-screen frame: the transcript viewport —
// committed transcript followed by the live tail (tool-group/stream/retry/
// plan/subagents/prompt), so those blocks scroll with the transcript
// instead of floating above a gap — then the pinned bottom chrome
// (spinner/dialog/editor/statusline/mode line) on the last rows.
func (m Model) fullscreenView() tea.View {
	if m.height <= 0 {
		// No WindowSizeMsg yet to size the viewport against; the inline
		// view degrades gracefully instead of drawing a zero-height frame.
		width := m.contentWidth()
		lines, editorTop, ruleSuppressed := m.liveLines(width)
		v := tea.NewView(strings.Join(lines, "\n"))
		if m.cfg.SessionName != "" {
			v.WindowTitle = m.cfg.SessionName
		}
		if editorTop >= 0 {
			if c := m.editor.Cursor(); c != nil {
				c.Position.X += m.margin()
				c.Position.Y += editorTop
				if ruleSuppressed {
					c.Position.Y--
				}
				v.Cursor = c
			}
		}
		return v
	}

	width := m.contentWidth()
	tail := m.liveTail(width)
	chrome, editorTop, _ := m.chromeLines(width, len(tail))
	if len(chrome) > m.height-1 {
		overflow := len(chrome) - (m.height - 1)
		chrome = chrome[overflow:]
		if editorTop >= 0 {
			editorTop -= overflow
			if editorTop < 0 {
				editorTop = -1
			}
		}
	}

	// A local copy: the persistent m.viewport only ever holds the
	// committed transcript (appendTranscript/layoutViewport keep it that
	// way), so folding the live tail in here — for display only — cannot
	// leak into state other code depends on. Same scroll-to-pause rule as
	// appendTranscript: a user scrolled up is not yanked back down by a
	// growing live tail.
	vp := m.viewport
	wasBottom := m.viewport.AtBottom()
	vp.SetContent(strings.Join(append(append([]string{}, m.transcript...), tail...), "\n"))
	if wasBottom {
		vp.GotoBottom()
	}

	vpRows := strings.Split(vp.View(), "\n")
	// viewport.View pads to its own Height; guard the invariant explicitly
	// rather than trust it silently, since a short content string is the
	// one case that could violate it.
	for len(vpRows) < vp.Height() {
		vpRows = append(vpRows, "")
	}
	if m.sel != nil {
		top := vp.YOffset()
		for i := range vpRows {
			if from, to, ok := m.sel.span(top+i, m.width); ok {
				vpRows[i] = highlightRow(vpRows[i], from, to)
			}
		}
	}

	content := append(append([]string{}, vpRows...), chrome...)
	v := tea.NewView(strings.Join(content, "\n"))
	if m.cfg.SessionName != "" {
		v.WindowTitle = m.cfg.SessionName
	}
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if editorTop >= 0 {
		if c := m.editor.Cursor(); c != nil {
			c.Position.X += m.margin()
			c.Position.Y += len(vpRows) + editorTop
			v.Cursor = c
		}
	}
	return v
}

// --- key handling ----------------------------------------------------------

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The `?` shortcuts panel is dismissed by any key
	// (docs/claude-code-reference.md §6); the key itself is not otherwise
	// acted on, so a stray Enter cannot submit while it is up.
	if m.shortcuts {
		m.shortcuts = false
		return m, nil
	}

	// Every keystroke but a fresh kill clears the "Ctrl+Y to paste deleted
	// text" hint (docs/claude-code-reference.md §2: "until the next
	// keystroke"). Reset here, unconditionally, then the editor.Update
	// path below re-arms it only when this key's own Event says a kill
	// just happened.
	m.justKilled = false

	if m.dialog != nil {
		consumed, shouldClose, cmd := m.dialog.HandleKey(msg)
		if shouldClose {
			m = m.closeDialog()
			return m, cmd
		}
		if consumed {
			return m, cmd
		}
	}

	// Fullscreen scroll keys, consumed here rather than routed through
	// viewport.Update (whose own KeyMap NewModel emptied for exactly this
	// reason): PgUp/PgDn and Shift+Up/Down scroll the transcript. Plain
	// arrows and j/k are deliberately NOT bound here — Up/Down on an empty
	// input are the editor's own history recall (see editor.Update below)
	// and j/k must still type — a deviation from the plan's looser
	// "arrow/j/k when the input is empty" wording, chosen so a scroll key
	// never silently swallows what the editor would otherwise have done
	// with it.
	if m.fullscreen {
		switch msg.String() {
		case "pgup":
			m.viewport.PageUp()
			return m, nil
		case "pgdown":
			m.viewport.PageDown()
			return m, nil
		case "shift+up":
			m.viewport.ScrollUp(1)
			return m, nil
		case "shift+down":
			m.viewport.ScrollDown(1)
			return m, nil
		}
	}

	// `r` cuts a pending retry's countdown short (docs/kiln-design-handoff/
	// README.md's "error" row: "r to retry now") — only while the live
	// retry block is up and the input is empty; otherwise `r` types
	// normally (a queued follow-up starting with the letter r must not be
	// eaten).
	if msg.String() == "r" && m.retry != nil && strings.TrimSpace(m.editor.Value()) == "" {
		if m.cfg.Lane != nil {
			m.cfg.Lane.RetryNow()
		}
		return m, nil
	}

	// `?` on an empty input shows the shortcuts panel
	// (docs/claude-code-reference.md §6, shortcuts.txt).
	if msg.String() == "?" && !m.busy && !m.prompt.Active() && strings.TrimSpace(m.editor.Value()) == "" {
		m.shortcuts = true
		return m, nil
	}

	// The autocomplete popup owns up/down/tab/enter/esc while it is open —
	// pi-tui's editor.js handleKey does exactly this before any of its own
	// submit/history/newline handling runs (see editor.js:599-645). Every
	// other key falls through to the router/editor pipeline below, and
	// refreshPopup rebuilds the popup from the buffer that pipeline
	// produces.
	if m.popup != nil {
		switch msg.String() {
		case "up":
			m.popup.Move(-1)
			return m, nil
		case "down":
			m.popup.Move(1)
			return m, nil
		case "tab", "enter":
			// A fully-typed slash command submitted with Enter runs on this
			// one keypress, as Claude Code does — the popup does not eat the
			// Enter to merely re-insert what is already there. A partial name
			// (or Tab) still completes first; a second Enter then submits.
			// The same goes for anything else already typed in full — an
			// argument ("/posture ops"), a file mention — when accepting
			// would leave the line as it is: Enter used to be swallowed there,
			// so the next message was typed onto the end of the command.
			item, hasItem := m.popup.SelectedItem()
			line, _ := m.editor.CursorLine()
			replacement, _ := m.popup.Accept()
			accepted := line
			if m.popup.Start >= 0 && m.popup.Start <= m.popup.End && m.popup.End <= len(line) {
				accepted = line[:m.popup.Start] + replacement + line[m.popup.End:]
			}
			if msg.String() == "enter" && hasItem && (strings.TrimSpace(line) == "/"+item.Value ||
				strings.TrimRight(accepted, " ") == strings.TrimRight(line, " ")) {
				m.popup = nil
				m.editor.PopupActive = false
				// Fall through to the editor's submit pipeline below.
			} else {
				replacement, _ := m.popup.Accept()
				m.editor.ReplaceCursorLine(m.popup.Start, m.popup.End, replacement)
				m.popup = nil
				m.editor.PopupActive = false
				return m, nil
			}
		case "esc":
			m.popup = nil
			m.editor.PopupActive = false
			return m, nil
		}
	}

	// Side effects the router's action closures record, applied to m
	// below. Router.Route calls these synchronously and returns before
	// this function does anything else with them, so there is no
	// staleness risk from rebuilding the router fresh on every key (unlike
	// keeping one long-lived Router closing over a Model value that
	// Update replaces on every call).
	var (
		didAbort      bool
		didClear      bool
		didClearScr   bool
		didExit       bool
		didCycle      bool
		didToggle     bool
		didRewind     bool
		didFullscreen bool
		hintMessage   string
		// promptEscInterrupt is set when Esc both declines the open
		// tool-permission prompt and must end the turn (defect 5): the
		// router's PermissionKey binding owns Esc ahead of everything
		// else while a prompt is up (keys.go's Route), so the router's
		// own "esc while busy -> Interrupt" case below never runs in
		// that case — without this, a busy turn with a prompt queued
		// behind it (concurrent subagents each raising their own bash
		// approval) can never be stopped by Esc, only Ctrl+C, which
		// disagrees with the design's stop() (Terminal.dc.html
		// lines 310-313: Esc clears the pending permission block AND
		// ends the turn). Scoped to a tool-permission prompt
		// (m.prompt.pending, not m.prompt.plan) — a plan prompt's own
		// Esc means "tell kiln what to change", not "decline and stop".
		promptEscInterrupt bool
	)
	router := NewRouter(KeyActions{
		PermissionKey: func(msg tea.KeyPressMsg) bool {
			if !m.prompt.Active() {
				return false
			}
			if m.busy && m.prompt.pending != nil && strings.EqualFold(msg.String(), "esc") {
				promptEscInterrupt = true
			}
			return m.prompt.HandleKey(msg)
		},
		IsBusy:              func() bool { return m.busy },
		HasInput:            func() bool { return strings.TrimSpace(m.editor.Value()) != "" },
		Interrupt:           func() { didAbort = true },
		ToggleExpanded:      func() { didToggle = true },
		CyclePermissionMode: func() { didCycle = true },
		ClearInput:          func() { didClear = true },
		ClearScreen:         func() { didClearScr = true },
		Rewind:              func() { didRewind = true },
		Exit:                func() { didExit = true },
		ToggleFullscreen:    func() { didFullscreen = true },
		// Hint's message replaces the mode line for modeHintDuration
		// (currently only Ctrl+C's "Press Ctrl-C again to exit" — see
		// modeHintText's doc comment); recorded here, applied below once
		// Route returns, the same pattern as every other action this
		// router closes over.
		Hint: func(message string) { hintMessage = message },
	})

	router.lastCtrlC = m.lastCtrlC
	router.lastEsc = m.lastEsc
	consumed := router.Route(msg)
	m.lastCtrlC = router.lastCtrlC
	m.lastEsc = router.lastEsc
	// A tool-permission prompt answered "no" (Esc, or feedback then Enter)
	// leaves its denied request on m.prompt.lastDenied (permissionview.go's
	// finishTool) — commit the "✕ Declined …" note now, before whatever
	// comes next (the feedback continuing the turn, or the turn simply
	// ending), matching docs/kiln-design-handoff/README.md's "note" row.
	if denied := m.prompt.lastDenied; denied != nil {
		m.prompt.lastDenied = nil
		m.commitNote(declinedNoteText(*denied, m.prompt.lastDeniedFeedback))
		m.prompt.lastDeniedFeedback = ""
		// The design prototype follows this note with a scripted "Okay, I
		// won't run it. What should I do instead?" from kiln. There a
		// script plays the model; here the model answers the refusal
		// itself, so a scripted line would put words in its mouth (it
		// said "run" of an edit, and landed before the real reply).
	}
	// "Yes, and switch to auto mode" (Bash) / "Yes, and switch to accept
	// edits" (Edit/Write) both allow the pending call AND change the
	// permission mode going forward — PromptState has no reference to the
	// gate, so it leaves the requested mode on switchMode (same
	// leave-it-for-the-caller pattern as lastDenied above) for this
	// handler to apply.
	if mode := m.prompt.switchMode; mode != "" {
		m.prompt.switchMode = ""
		if m.cfg.Gate != nil {
			m.cfg.Gate.SetMode(claudesettings.PermissionMode(mode))
			m.footer.SetNote("")
			modeStr := mode
			m.footer.Apply(StatusPatch{Mode: &modeStr})
		}
	}
	m = m.syncPromptPlaceholder()
	if consumed {
		var hintCmd tea.Cmd
		if hintMessage != "" {
			m.modeHintGen++
			gen := m.modeHintGen
			m.modeHintText = hintMessage
			hintCmd = tea.Tick(modeHintDuration, func(time.Time) tea.Msg { return msgClearModeHint{gen: gen} })
		}
		if (didAbort || promptEscInterrupt) && m.cfg.Lane != nil {
			_ = m.cfg.Lane.Abort()
		}
		if didClear {
			m.editor.SetValue("")
		}
		if didCycle {
			m = m.cycleMode()
		}
		if didRewind && strings.TrimSpace(m.editor.Value()) == "" {
			m = m.openRewind()
		}
		if didExit {
			return m, tea.Quit
		}
		if didToggle {
			return m.toggleVerbose()
		}
		if didFullscreen {
			return m.toggleFullscreen()
		}
		if didClearScr {
			return m, tea.ClearScreen
		}
		m = m.refreshPopup()
		return m, hintCmd
	}

	ed, cmd, ev := m.editor.Update(msg)
	m.editor = ed
	m = m.refreshPopup()
	switch ev.Kind {
	case editor.EventSubmit:
		return m.handleSubmit(ev.Text)
	case editor.EventCancel:
		// Idle Esc with no prompt/turn up: nothing to cancel but the
		// router's own double-press/rewind bookkeeping, already handled
		// above.
		return m, cmd
	case editor.EventKilled:
		m.justKilled = true
		return m, cmd
	}
	return m, cmd
}

// syncPromptPlaceholder sets the editor's placeholder for the current
// prompt/busy state (docs/kiln-design-handoff/README.md "Interactions"):
// "press 1, 2 or 3" while a tool-permission or plan prompt is up (4 options
// for the Bash prompt's extra "switch to auto mode" choice), the busy text
// while a turn is running, otherwise the idle default. Called after every
// keystroke the router may have changed prompt state on, and on prompt
// open (MsgPermissionPrompt/MsgPlanPrompt).
func (m Model) syncPromptPlaceholder() Model {
	switch {
	case m.prompt.feedback != nil:
		// The prompt's own field is taking the typing; saying "press 1, 2
		// or 3" here pointed at a choice that is no longer open. Esc means
		// different things in the two fields (permissionview.go).
		if m.prompt.plan != nil {
			m.editor.SetPlaceholder("typing in the prompt above · enter to send · esc to go back")
		} else {
			m.editor.SetPlaceholder("typing in the prompt above · enter to send · esc to decline")
		}
	case m.prompt.pending != nil:
		m.editor.SetPlaceholder(placeholderForOptionCount(len(promptOptionsFor(m.prompt.pending.request.ToolName))))
	case m.prompt.plan != nil:
		m.editor.SetPlaceholder("press 1, 2 or 3")
	case m.busy:
		m.editor.SetPlaceholder("queue a follow-up, or esc to stop")
	default:
		m.editor.SetPlaceholder(editor.DefaultPlaceholder)
	}
	return m
}

// placeholderForOptionCount renders the "press 1, 2[, 3] or N" hint for a
// prompt's actual option count (3 for generic/edit/write, 4 for Bash's
// extra "switch to auto mode" choice) rather than a hardcoded tool-name
// check, so any future variant with a different count is covered for
// free.
func placeholderForOptionCount(n int) string {
	if n <= 1 {
		return "press 1"
	}
	digits := make([]string, n)
	for i := range digits {
		digits[i] = fmt.Sprintf("%d", i+1)
	}
	return "press " + strings.Join(digits[:n-1], ", ") + " or " + digits[n-1]
}

// refreshPopup rebuilds the autocomplete popup from the editor's current
// line and cursor column, called after every keystroke that could have
// changed either — editor.js's own updateAutocomplete/tryTriggerAutocomplete
// run on the same "after every edit" cadence (editor.js:1016-1045).
func (m Model) refreshPopup() Model {
	line, col := m.editor.CursorLine()
	m.popup = BuildPopup(m.cfg.Registry, m.cfg.Cwd, line, col)
	m.editor.PopupActive = m.popup != nil
	return m
}

// cycleMode advances the permission mode one step around
// permissionModeRing. A mode not in the ring (bypassPermissions, dontAsk)
// goes straight to auto, the ring's first entry, rather than panicking on a
// not-found index or silently no-op'ing.
func (m Model) cycleMode() Model {
	if m.cfg.Gate == nil {
		return m
	}
	cur := string(m.cfg.Gate.Mode())
	next := permissionModeRing[0]
	for i, mode := range permissionModeRing {
		if mode == cur {
			next = permissionModeRing[(i+1)%len(permissionModeRing)]
			break
		}
	}
	m.cfg.Gate.SetMode(claudesettings.PermissionMode(next))
	m.footer.SetNote("")
	nextStr := next
	m.footer.Apply(StatusPatch{Mode: &nextStr})
	return m
}

// --- submitting a line ------------------------------------------------------

func (m Model) handleSubmit(line string) (tea.Model, tea.Cmd) {
	line = strings.TrimSpace(line)
	if line == "" {
		return m, nil
	}
	m.editor.AddToHistory(line)
	if m.cfg.HistoryPath != "" {
		editor.Append(m.cfg.HistoryPath, line)
	}

	// A follow-up typed while a turn is running (and no prompt is waiting
	// on an answer — that keystroke belongs to the prompt, never to a new
	// message) queues instead of starting a second turn
	// (docs/kiln-design-handoff/README.md's "Queued follow-up"): it shows
	// in the live region with a "queued" meta (liveTail, RenderQueuedFollowUp)
	// rather than committing to the transcript right away — committing it
	// immediately read out of order, above the reply to the turn it
	// interrupted (defect 20260926T232249Z-queued-block-order). Lane.Steer
	// still queues the raw text for the harness's own next-turn injection
	// (harness/lane.go's pi.lane.state.inbox); MsgQueue{Len:0} (the lane's
	// own drain, EventQueueUpdate) is what actually commits it, as an
	// ordinary `you` block with no meta, once the lane has re-parented it
	// onto the branch.
	if m.busy && !m.prompt.Active() {
		m.queued = append(m.queued, line)
		if m.cfg.Lane != nil {
			if err := m.cfg.Lane.Steer(line); err != nil {
				m.commit(RenderError(err.Error()))
			}
		}
		m.editor.SetValue("")
		return m, nil
	}

	width := m.contentWidth()
	echo := func() {
		m.commit(RenderUserMessage(line, width))
	}

	if classified, ok := ClassifyInput(line); ok {
		echo()
		return m, m.runMode(classified)
	}

	// A bare /rewind opens the same picker as esc esc; the command's text
	// listing is for print mode, where there is no picker.
	if strings.TrimSpace(line) == "/rewind" && m.cfg.Lane != nil {
		m.editor.SetValue("")
		return m.openRewind(), nil
	}

	ctx := context.Background()
	var handled *commands.Result
	if m.cfg.Registry != nil {
		result, err := m.cfg.Registry.Execute(ctx, line)
		if err != nil {
			m.commit(RenderError(err.Error()))
			return m, nil
		}
		handled = result
	}
	if handled != nil {
		if handled.Exit {
			echo()
			return m, tea.Quit
		}
		if handled.Clear {
			// The conversation was reset: redraw the (now empty)
			// transcript, then show the command's note under it.
			note := strings.Join(handled.Output, " ")
			return m, tea.Sequence(tea.ClearScreen, func() tea.Msg {
				return msgReplayTranscript{then: func() {
					if m.cfg.Bridge != nil && note != "" {
						m.cfg.Bridge.CommitNote(note)
					}
				}}
			})
		}
		if handled.Modal != nil {
			// Echoed when the dialog closes — see dialogEcho.
			m.dialogEcho = line
			m.dialog = NewCommandDialog(*handled.Modal)
			return m, nil
		}
		echo()
		if handled.Exec != nil {
			// Hand the terminal to the program (an editor for /memory):
			// the TUI suspends, the program runs in the foreground, and
			// the note lands once it exits.
			done := handled.ExecDone
			return m, tea.ExecProcess(handled.Exec, func(err error) tea.Msg {
				if done == nil {
					return nil
				}
				return msgExecDone{note: done(err)}
			})
		}
		switch {
		case handled.Context != nil:
			// /context gets the structured "context" block
			// (context.go) instead of its plain Output rows — the
			// stacked bar and legend are the whole point of the
			// command in the kiln design (docs/kiln-design-handoff/
			// README.md, "context" row).
			m.commitSynthetic(append([]string{""}, RenderContext(*handled.Context, width)...))
		case len(handled.Output) == 1:
			// A single-line result reads as a system note in the kiln
			// design ("/cost", "/compact", "/model", "/agents",
			// "/help" info form — docs/kiln-design-handoff/README.md's
			// "note" row example copy), not a "⎿ " continuation under
			// the echo.
			m.commitNote(handled.Output[0])
		case len(handled.Output) > 0:
			m.commitCommandResult(handled.Name, handled.Output)
		}
		if handled.Prompt == "" {
			return m, nil
		}
	} else {
		echo()
	}

	prompt := line
	var images []msg.ImageContent
	if handled != nil && handled.Prompt != "" {
		prompt = handled.Prompt
	} else if m.cfg.ResolveMentions != nil {
		resolvedPrompt, resolvedImages, describe := m.cfg.ResolveMentions(ctx, line)
		if resolvedPrompt != "" {
			prompt = resolvedPrompt
		}
		images = resolvedImages
		if len(describe) > 0 {
			m.commit(append(describe, ""))
		}
	}

	var hookContext []string
	if m.cfg.RunPromptHooks != nil {
		blocked, hctx := m.cfg.RunPromptHooks(ctx, line)
		if blocked != "" {
			m.commit(RenderError("blocked by hook: " + blocked))
			return m, nil
		}
		hookContext = hctx
	}
	if len(m.startupContext) > 0 {
		hookContext = append(append([]string{}, m.startupContext...), hookContext...)
		m.startupContext = nil
	}
	if len(hookContext) > 0 {
		prompt = "<hook-context>\n" + strings.Join(hookContext, "\n\n") + "\n</hook-context>\n\n" + prompt
	}

	return m.beginTurn(prompt, images)
}

// runMode executes a `!` or `#` line and returns a Cmd that commits its
// output. `!` may take real time (a build, a slow command), so it runs on
// its own goroutine rather than blocking Update; `#` is fast local file
// I/O and is committed synchronously by the caller instead — see
// handleSubmit, which never calls runMode for ModeMemory.
func (m Model) runMode(c Classified) tea.Cmd {
	switch c.Mode {
	case ModeBang:
		env := m.cfg.Env
		cwd := m.cfg.Cwd
		bridge := m.cfg.Bridge
		width := m.contentWidth()
		return func() tea.Msg {
			lines := RunBang(context.Background(), c.Body, env, width)
			if bridge != nil {
				// Runs on its own Cmd goroutine, not Update's — Bridge's
				// own freeze helper (not the Model one, which needs the
				// Update goroutine's m) is the one safe to call here.
				bridge.FreezeBefore()
				bridge.Commit(lines)
			}
			_ = cwd
			return nil
		}
	case ModeMemory:
		// A system note of its own, not rows under the user's "you" block.
		note, ok := AddMemory(c.Body, m.cfg.Cwd)
		if !ok {
			m.commit(RenderError(note))
			break
		}
		m.commitNote(note)
	}
	return nil
}

func (m Model) beginTurn(prompt string, images []msg.ImageContent) (tea.Model, tea.Cmd) {
	m.busy = true
	m.turnStartedAt = time.Now()
	// Seeded from zero, as app.ts's `spinner.start(turn++)` is, so the
	// first turn picks the same gerund as the TS oracle.
	m.spinner.Start(m.turn)
	m.turn++
	m.footer.SetBusy(true)
	m.editor.SetPlaceholder("queue a follow-up, or esc to stop")
	// A fresh turn starts with no dispatches: the previous turn's
	// subagents panel (if any) does not linger into this one.
	m.subagents.Reset()
	m.plan.Reset()

	lane := m.cfg.Lane
	bridge := m.cfg.Bridge
	gitFn := m.cfg.GitStatus
	startedAt := m.turnStartedAt
	if bridge != nil {
		bridge.ResetTurnCounters()
	}

	cmd := func() tea.Msg {
		result := harness.RunResult{}
		var err error
		if lane != nil {
			result, err = lane.Prompt(context.Background(), prompt, images)
		}
		seconds := int(time.Since(startedAt).Round(time.Second) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		if gitFn != nil && bridge != nil {
			if status, ok := gitFn(context.Background()); ok {
				bridge.Send(MsgGitStatus{Status: status})
			}
		}
		return msgTurnResult{result: result, err: err, seconds: seconds}
	}
	return m, tea.Batch(cmd, tickCmd())
}

// --- layout -----------------------------------------------------------------

// contentWidth is ContentWidth(m.width) — the 2-column side margin
// (layout_margin.go) already subtracted, so every caller that wraps or
// fits to it draws inside the margin without knowing the margin exists.
func (m Model) contentWidth() int {
	return ContentWidth(m.width)
}

// margin is marginFor(m.width): the left-pad every rendered row needs
// (layout_margin.go's padMargin), applied at the points content leaves this
// package for the terminal — see layout_margin.go's doc comment for the
// full list.
func (m Model) margin() int {
	return marginFor(m.width)
}

func (m Model) liveEditorWidth() int {
	return m.contentWidth()
}

// frameHeight is the terminal height available to whatever sizes itself
// against it (dialogRows, renderPopup): m.height, falling back to 24 when
// no WindowSizeMsg has arrived yet, minus one row in fullscreen to reserve
// the viewport's own bottom row.
func (m Model) frameHeight() int {
	h := m.height
	if h <= 0 {
		return 24
	}
	if m.fullscreen {
		h--
	}
	return h
}

// bannerRows renders cfg.Banner fitted to the content width, closed with a
// full-width `─` divider (design_handoff "Banner": it "sits above a `─`
// rule"). Factored out of the WindowSizeMsg handler so replayTranscript can
// re-commit the same rows in fullscreen, where the banner lives in the
// transcript buffer rather than native scrollback and is lost on every
// clear+replay unless re-added.
func (m Model) bannerRows() []string {
	src := m.cfg.Banner
	if m.cfg.BannerFunc != nil {
		src = m.cfg.BannerFunc()
	}
	rows := make([]string, len(src))
	for i, r := range src {
		rows[i] = FitStatus(r, m.contentWidth())
	}
	ruleCh := "─"
	if IsPlain() {
		ruleCh = "-"
	}
	rows = append(rows, Rule(strings.Repeat(ruleCh, m.contentWidth())))
	// Not margin-padded here: every caller commits this through
	// m.cfg.Bridge.Commit, which applies the left margin once, centrally
	// (see Commit's own doc comment in bridge.go) — padding here too would
	// double it.
	return rows
}

// View composes the live region: spinner, live thinking, permission/plan
// prompt (or, in its place, the hint row), the editor frame, then the mode
// line — docs/claude-code-reference.md §§1-2: no status row by default, the
// bottom area is the input box and the mode line only. Everything here is
// redrawn every frame; the committed transcript lives in real scrollback
// and is never touched.
func (m Model) View() tea.View {
	if m.fullscreen {
		return m.fullscreenView()
	}
	width := m.contentWidth()
	lines, editorTop, ruleSuppressed := m.liveLines(width)

	// The live region renders at its natural height directly below the
	// committed transcript. An earlier version padded it to fill the
	// terminal so the input box sat on the last rows (like Claude Code),
	// but a full-height live region is fundamentally incompatible with
	// Bubbletea's inline renderer: tea.Println/insertAbove scrolls the
	// whole (full-height) region, overpainting the committed banner and
	// leaving blank residue at the bottom, and the constant-height padding
	// only masked a separate insertAbove cursor bug (fixed in
	// third_party/bubbletea/cursed_renderer.go). To match Claude Code's
	// startup screen (input box near the bottom) the harness instead
	// commits blank filler lines once at startup (see WindowSizeMsg), which
	// is real scrollback and keeps the live region small.
	v := tea.NewView(strings.Join(lines, "\n"))
	if m.cfg.SessionName != "" {
		v.WindowTitle = m.cfg.SessionName
	}
	if editorTop >= 0 {
		if c := m.editor.Cursor(); c != nil {
			c.Position.X += m.margin()
			c.Position.Y += editorTop
			if ruleSuppressed {
				c.Position.Y--
			}
			v.Cursor = c
		}
	}
	return v
}

// commitReconnectNote commits "↺ Reconnected on attempt N" the first time
// anything is about to commit after a retry succeeded (MsgRetryStart set
// reconnectedAttempt), then clears it so it fires exactly once per retry —
// ahead of the streamed message's first live delta or, if the message never
// streamed a delta before completing, ahead of its committed markdown block
// (both call sites go through this helper: the MsgStreamText and
// msgCommitMarkdown cases in update).
func (m Model) commitReconnectNote() Model {
	if m.reconnectedAttempt == 0 {
		return m
	}
	attempt := m.reconnectedAttempt
	m.reconnectedAttempt = 0
	m.commitNote(fmt.Sprintf("↺ Reconnected on attempt %d", attempt))
	return m
}

// renderRetryLive draws the live "error" retry block (retry.go's
// RenderRetry) plus a trailing blank row, or nil when no retry is pending —
// same append-unconditionally contract as renderPlanLive/subagents.Render.
// now is HARNESS_TEST_CLOCK's override when set, so a PTY golden's
// countdown is deterministic instead of racing real wall-clock time.
func (m Model) renderRetryLive(width int) []string {
	if m.retry == nil {
		return nil
	}
	now := time.Now()
	if t, ok := clockOverride(); ok {
		now = t
	}
	return append([]string{""}, RenderRetry(*m.retry, now, width)...)
}

// renderStreamLive draws the live streaming "kiln" block (stream.go's
// RenderStreamLive) plus a trailing blank row, or nil when nothing has
// streamed yet (before the first delta, or after msgCommitMarkdown/turn end
// cleared it) or in plain (screen-reader) mode, which never shows it (D's
// spec: a screen reader would re-read the live region every tick).
func (m Model) renderStreamLive(width int) []string {
	if m.streamText == "" || IsPlain() {
		return nil
	}
	return append([]string{""}, RenderStreamLive(m.streamText, width, maxStreamRows)...)
}

// renderPlanLive draws the live "plan" checklist (plan.go's RenderPlan)
// plus a trailing blank row, or nil when no plan is active — so callers can
// append its result unconditionally, matching m.subagents.Render's own
// contract.
func (m Model) renderPlanLive(width int) []string {
	items := m.plan.Live()
	if len(items) == 0 {
		return nil
	}
	return append([]string{""}, RenderPlan(items, width)...)
}

// liveTail builds the rows that are "the newest part of the transcript":
// the in-flight tool-group row, streaming text, the retry countdown, the
// live plan checklist, the subagents panel and the permission/plan prompt.
// In fullscreen these scroll with the transcript (they sit in the viewport,
// directly after the last committed block) rather than pinning above the
// bottom chrome; in inline mode they still render immediately above it,
// via liveLines. A dialog or the verbose bridge view replace everything
// from the prompt onward, so only the tool-group/thinking rows lead in
// that case.
func (m Model) liveTail(width int) []string {
	var lines []string

	// The tool-group's own live row (RenderToolGroupRunning, "● Running N
	// shell commands…") only exists while m.group is accumulating, which
	// is only ever true while a turn is running (flushGroup empties it
	// before finishTurn clears m.busy, and MsgPermissionPrompt/
	// MsgPlanPrompt both flush it before a prompt can open) — so any time
	// this row would show, the busy line below it is already up and
	// naming the same in-flight work (defect 4: a real 120x40 session
	// showed both "● Running 2 shell commands…" and "◐ Running cd
	// /private/tmp/…  43s · 7.1k tokens   esc to stop" at once; the
	// design has only the busy line, Terminal.dc.html line 125). Suppress
	// the live group row while busy rather than dropping the
	// accumulation itself — flushGroup still commits every call's full
	// block once the group ends, so nothing is lost from scrollback,
	// only the redundant live summary.
	if m.group != nil && !m.busy {
		lines = append(lines, "", RenderToolGroupRunning(m.group.kind, len(m.group.views)))
	}
	if m.thinking != nil {
		view := *m.thinking
		view.Expanded = false
		if collapsed := RenderThinking(view); len(collapsed) > 0 {
			lines = append(lines, FitStatus(collapsed[0], width))
		}
	}

	// Follow-ups queued via Lane.Steer while this turn (or an aborted one
	// whose queued items the lane has not drained yet, see m.queued's doc
	// comment) is busy: shown here, below the running turn, until the
	// lane's own drain commits them for real (MsgQueue{Len:0} in Update).
	for _, text := range m.queued {
		lines = append(lines, RenderQueuedFollowUp(text, width)...)
	}

	if m.dialog != nil || (m.cfg.Bridge != nil && m.cfg.Bridge.Verbose()) {
		return padMargin(lines, m.margin())
	}

	// The live-while-last blocks (live_freeze.go): streaming text and
	// the retry countdown only show while nothing is waiting on the
	// user (a permission/plan prompt pauses the turn); the plan
	// checklist and the subagents panel keep showing during a prompt
	// too — the design's permission scene (docs/kiln-design-handoff/
	// README.md scene 06) keeps them visible, in the transcript
	// position, directly above the "approval needed" block and the
	// busy line.
	if !m.prompt.Active() {
		if rows := m.renderStreamLive(width); len(rows) > 0 {
			lines = append(lines, rows...)
		}
		if rows := m.renderRetryLive(width); len(rows) > 0 {
			lines = append(lines, rows...)
		}
	}
	if rows := m.renderPlanLive(width); len(rows) > 0 {
		lines = append(lines, rows...)
	}
	if rows := m.subagents.Render(width); len(rows) > 0 {
		lines = append(lines, "")
		lines = append(lines, rows...)
	}

	if m.prompt.Active() {
		// Every live block leads with a blank row, like a committed one.
		lines = append(lines, "")
		// The prompt renders where the input box normally sits, but —
		// unlike before this pass — the busy line, editor and status
		// row below it all keep rendering too (docs/kiln-design-
		// handoff/README.md scene 06): "approval needed" block, then
		// "◐ Waiting for approval…", then the input with "press 1, 2
		// or 3", then the status line. Plan approval uses the same
		// block anatomy, so it gets no chrome of its own here.
		lines = append(lines, m.prompt.Render(width)...)
		lines = append(lines, "")
	}

	// The busy line is set off from the transcript by one blank row, as
	// every block is from the next (the design's 14px block gap: the
	// transcript's bottom padding plus the busy row's own). Only the
	// permission prompt used to end with one, so the busy line sat flush
	// under streamed text and tool blocks everywhere else.
	if len(m.spinner.Render(width, time.Time{})) > 0 &&
		(len(lines) == 0 || strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) != "") {
		lines = append(lines, "")
	}

	return padMargin(lines, m.margin())
}

// chromeLines builds the bottom chrome that stays pinned regardless of
// scroll position — busy line, popup, input box and status line (or, in
// their place, a dialog or the verbose bridge's notice row) — and the
// index of the editor's first row within this slice (editorTop, -1 when
// the editor is not shown). tailLen is the number of rows liveTail
// produced immediately above this chrome (in inline mode, and previously
// always) and is only used to size a dialog's remaining room and to
// position a popup relative to the input box's top rule, exactly as
// liveLines used to when tail and chrome were one slice.
//
// ruleSuppressed reports whether the editor's own top rule was dropped —
// see the call site below and transcriptIsEmpty's doc comment; the two
// View/fullscreenView callers use it to adjust the hardware cursor's row
// by the one line that convention assumes is always there.
func (m Model) chromeLines(width, tailLen int) (lines []string, editorTop int, ruleSuppressed bool) {
	editorTop = -1

	switch {
	case m.dialog != nil:
		// A dialog replaces the input box and mode line under a `▔` rule
		// carrying the effort indicator (docs/claude-code-reference.md §5).
		if s := m.spinner.Render(width, time.Time{}); len(s) > 0 {
			lines = append(lines, s...)
		}
		lines = append(lines, m.dialogRows(width, tailLen+len(lines))...)
	case m.cfg.Bridge != nil && m.cfg.Bridge.Verbose():
		// The detailed transcript view (verbose-ctrl-o.txt rows 38-39): a
		// rule and the notice row take the input box's place until Ctrl+O
		// toggles back.
		if s := m.spinner.Render(width, time.Time{}); len(s) > 0 {
			lines = append(lines, s...)
		}
		lines = append(lines, RuleColour(strings.Repeat(RuleFillChar(), width)), m.renderStatusRow(width))
	default:
		// The busy line always sits directly above the input box — the
		// last thing before it, whatever else is showing above (streaming
		// text, a retry, the plan/subagents blocks, or the permission/plan
		// prompt itself).
		if s := m.spinner.Render(width, time.Time{}); len(s) > 0 {
			lines = append(lines, s...)
		}

		if !m.prompt.Active() && m.popup != nil {
			// Claude Code draws the suggestions directly above the input
			// box's top rule (autocomplete-slash.txt rows 29-32).
			lines = append(lines, m.renderPopup(width, tailLen+len(lines))...)
		}

		editorTop = len(lines)
		editorRows := m.editor.View(width)
		// A fresh start, or a resumed session before any turn, has
		// nothing between the banner's own closing rule (already
		// committed, permanently, to scrollback) and this box: in inline
		// mode that puts two hairlines on consecutive rows with no
		// content between them (*qa/findings/20260927T022144Z-inline-
		// stacked-rules.json*). Dropping the box's own top rule in that
		// one case leaves the banner's rule to do the same job alone —
		// exactly one hairline, same as once a turn has committed
		// something here (the ordinary case, untouched). Fullscreen never
		// takes this branch: its viewport pads to its own height
		// regardless of content, so the two rules are never adjacent
		// there, and this must not fire while replaying into it.
		//
		// bannerDone/len(Banner)>0 guard the case there is no banner rule
		// to double up with at all (a bare Model in a unit test, or a
		// config with no banner configured) — transcriptIsEmpty alone
		// cannot tell "nothing but the banner committed" from "nothing
		// has committed, and there is no banner either", and only the
		// former should drop this rule. tailLen==0/len(lines)==0 guard
		// the busy spinner/popup case: once either shows, it is itself
		// the separating content between the two rules, same as
		// committed transcript text would be, so the box's own rule
		// stays.
		if !m.fullscreen && m.bannerDone && len(m.cfg.Banner) > 0 && m.transcriptIsEmpty() && tailLen == 0 && len(lines) == 0 && len(editorRows) > 0 {
			editorRows = editorRows[1:]
			ruleSuppressed = true
		}
		lines = append(lines, editorRows...)
		if m.shortcuts {
			// The shortcuts panel takes the status row's place
			// (shortcuts.txt rows 32-39).
			lines = append(lines, RenderShortcuts(width)...)
		} else {
			// The one-row status line sits directly below the input box
			// (docs/kiln-design-handoff/README.md "Screen anatomy") and is
			// the whole bottom area — kiln's own status line is the design.
			// A configured Claude Code settings.json "statusLine" command
			// used to render as extra dim rows underneath it; that plumbing
			// (Config.StatusLineCommand, Model.statusLine,
			// refreshStatusLine, msgStatusLine/msgStatusLineTick) has been
			// removed entirely — there is no opt-in setting yet to bring it
			// back, so a configured command is now silently unused.
			lines = append(lines, m.renderStatusRow(width))
		}
	}

	// Padding here (rather than per-case above) is the "one place" this
	// port tries to keep the margin in: every case above already renders
	// at width == m.contentWidth(), so the only thing left to add is the
	// left-shift itself, once, on the way out — padMargin doesn't touch
	// row count, so editorTop (an index into lines) stays valid.
	return padMargin(lines, m.margin()), editorTop, ruleSuppressed
}

// liveLines builds the live-region rows (everything below the committed
// transcript: spinner, dialog/prompt, input box, statusLine, mode line) and
// the index of the editor's first row (editorTop, -1 when the editor is not
// shown). Split out of View so WindowSizeMsg can measure the live region's
// height to size the startup filler. This is liveTail followed by
// chromeLines — in inline mode the two always render together, back to
// back; fullscreen instead folds liveTail into the scrolling viewport (see
// fullscreenView) and keeps only chromeLines pinned to the bottom.
func (m Model) liveLines(width int) (lines []string, editorTop int, ruleSuppressed bool) {
	tail := m.liveTail(width)
	chrome, chromeEditorTop, chromeRuleSuppressed := m.chromeLines(width, len(tail))

	lines = make([]string, 0, len(tail)+len(chrome))
	lines = append(lines, tail...)
	lines = append(lines, chrome...)

	editorTop = -1
	if chromeEditorTop >= 0 {
		editorTop = len(tail) + chromeEditorTop
	}
	return lines, editorTop, chromeRuleSuppressed
}

// dialogRows renders the open dialog under its `▔` rule, sized to what is
// left of the terminal below linesAbove.
func (m Model) dialogRows(width, linesAbove int) []string {
	height := m.frameHeight()
	room := height - linesAbove - 2
	if room < 4 {
		room = 4
	}
	// A blank row first, as between any two blocks: the rule otherwise sat
	// directly under the last line of the reply above it.
	rows := []string{"", DialogTopRule(m.dialog, width)}
	for _, r := range m.dialog.Render(width, room) {
		rows = append(rows, FitStatus(r, width))
	}
	return rows
}

// toggleVerbose flips the bridge's verbose flag on Ctrl+O, clears the
// screen and replays the transcript so far at the new verbosity
// (docs/claude-code-reference.md §3; Bridge.MsgClearAndReplay documents
// the sequence).
func (m Model) toggleVerbose() (tea.Model, tea.Cmd) {
	if m.cfg.Bridge == nil {
		return m, nil
	}
	m.cfg.Bridge.SetVerbose(!m.cfg.Bridge.Verbose())
	return m, tea.Sequence(tea.ClearScreen, func() tea.Msg { return msgReplayTranscript{} })
}

// commitBanner commits cfg.Banner (fitted, with its closing rule) and, for a
// resumed session, replays the prior transcript right after it — the
// startup work bannerBackgroundGrace defers off the first WindowSizeMsg (or
// tea.BackgroundColorMsg's handler runs early, if the reply lands first).
// A no-op once bannerDone is set, so whichever of those two call sites
// reaches it first is the only one that commits anything.
func (m Model) commitBanner() Model {
	if m.bannerDone {
		return m
	}
	if m.cfg.Bridge != nil && len(m.cfg.Banner) > 0 {
		rows := m.bannerRows()
		// The input box sits directly below the banner in inline mode. (No
		// bottom-pinning filler: on a tall terminal it opens a huge void
		// and, as the statusline loads and notices commit, scrolls the
		// banner off the top.) In fullscreen the banner is the transcript's
		// first content instead — appendTranscript (via the bridge's
		// fullscreen sink) puts it at the top of the viewport, not
		// scrollback.
		m.cfg.Bridge.Commit(rows)
		m.bannerRowCount = len(rows)
	}
	if m.cfg.IsResume {
		// A resumed session (--resume/--continue) starts with a populated
		// Lane but nothing yet committed to the transcript: without this,
		// the screen shows the banner, its closing rule, a blank live
		// region and then the input box's own top rule — two rules with no
		// conversation between them (defect *resumed-session-no-
		// transcript-replay). Replay uses the exact same renderer Ctrl+O
		// does (RenderTranscriptEntries via replayTranscript), just
		// triggered once here instead of by a later toggle, so nothing
		// double-renders when Ctrl+O or a resize replay runs later.
		//
		// A resumed session whose lane has no entries yet (never had a
		// turn) replays nothing, leaving committedRows at bannerRowCount —
		// transcriptIsEmpty then reports true, exactly as for a fresh
		// start (*qa/findings/20260927T022144Z-inline-stacked-rules.json*).
		m.replayTranscript()
	}
	m.bannerDone = true
	return m
}

// transcriptIsEmpty reports whether nothing but the banner has been
// committed to the transcript yet — a fresh start, or a resumed session
// whose lane had no entries to replay. committedRows (incremented off
// every tea.PrintedLines echo, whichever call site committed it) already
// counts every row printed since the last clear; bannerRowCount is what
// the banner's own commit alone contributed, captured once in
// commitBanner. Used by chromeLines (inline mode only) to suppress the
// input box's own top rule in that state, so it does not stack a second
// hairline directly under the banner's closing rule with nothing between
// them.
func (m Model) transcriptIsEmpty() bool {
	return m.committedRows <= m.bannerRowCount
}

// replayTranscript re-commits every entry on the lane's current branch,
// rendered at the current width and verbosity. Committed rows live in
// scrollback and cannot be repainted, so a verbosity change redraws from
// the session log, the same source Claude Code redraws from.
//
// The banner needs the same treatment: a clear wipes it along with
// everything else on screen (in fullscreen it is part of the transcript
// buffer), so it is re-committed first — ahead of the entries — or
// Ctrl+O, /clear and the resize rewrap would each drop it. commitBanner's
// own replay runs before bannerDone is set, so it is not committed twice.
func (m Model) replayTranscript() {
	if m.cfg.Bridge == nil || m.cfg.Lane == nil {
		return
	}
	if m.bannerDone && len(m.cfg.Banner) > 0 {
		m.cfg.Bridge.Commit(m.bannerRows())
	}
	entries, err := m.cfg.Lane.FindEntries(context.Background())
	if err != nil {
		m.cfg.Bridge.Commit(RenderError(err.Error()))
		return
	}
	// A resumed session whose lane has no entries yet (never had a turn)
	// commits nothing here, leaving committedRows at bannerRowCount —
	// transcriptIsEmpty then reports true, exactly as for a fresh start
	// (*qa/findings/20260927T022144Z-inline-stacked-rules.json*).
	m.cfg.Bridge.Commit(RenderTranscriptEntries(oldestFirst(entries), m.contentWidth(), m.cfg.Bridge.Verbose(), m.cfg.Cwd, m.cfg.Bridge.Synthetics()))
}

// openRewind opens the Rewind dialog over the lane's user messages
// (docs/claude-code-reference.md §5, dialog-rewind.txt). Choosing a message
// navigates the session tree to just before it and redraws the transcript.
func (m Model) openRewind() Model {
	if m.cfg.Lane == nil {
		return m
	}
	entries, err := m.cfg.Lane.FindEntries(context.Background())
	if err != nil {
		if m.cfg.Bridge != nil {
			m.cfg.Bridge.Commit(RenderError(err.Error()))
		}
		return m
	}
	lane := m.cfg.Lane
	m.dialog = NewRewindDialog(RewindEntriesFromSession(entries), func(entryID string) error {
		if err := lane.NavigateTree(context.Background(), parentOf(entries, entryID)); err != nil {
			return err
		}
		// The transcript redraw and the "Rewound to before" note follow
		// from the dialog's result (msgDialogResult.replay). A Send from
		// here would deadlock: this runs inside Update.
		return nil
	})
	return m
}

// oldestFirst reverses Lane.FindEntries's newest-first order.
func oldestFirst(entries []session.Entry) []session.Entry {
	out := make([]session.Entry, len(entries))
	for i, e := range entries {
		out[len(entries)-1-i] = e
	}
	return out
}

// parentOf returns the parent id of entryID (nil for a root entry or an
// unknown id): the point "before" that message the Rewind dialog promises.
func parentOf(entries []session.Entry, entryID string) *string {
	for _, e := range entries {
		if e.ID == entryID {
			return e.ParentID
		}
	}
	return nil
}

// renderPopup renders the `/`/`@` autocomplete list directly below the
// editor's bottom rule, inside the live region, where pi-tui's editor
// draws it (components/editor.js render(): the list follows
// renderBottomBorder). linesAbove is how many rows View has already drawn
// (spinner/thinking/prompt/editor) before the popup's own slot; together
// with the footer's fixed two rows, it bounds how many popup rows fit so the whole frame
// never exceeds the terminal's height — pi-tui's own
// autocompleteMaxVisible (3..20, default 5) is a widget-level clamp, not a
// terminal-height one, so this cap is this port's own addition for the
// no-room case the task calls out explicitly.
func (m Model) renderPopup(width, linesAbove int) []string {
	height := m.frameHeight()
	// The bottom area is one row now (the mode line only — no status row,
	// docs/claude-code-reference.md §1).
	const footerRows = 1
	// A `─` rule in the rule colour sits directly above the popup rows
	// (docs/kiln-design-handoff/README.md "Screen anatomy": "a list
	// directly above the input with a `─` rule above it"), so it counts
	// against the same room budget as the rows themselves.
	room := height - linesAbove - footerRows - 1
	if room < 1 {
		return nil
	}
	maxRows := 5
	if maxRows > room {
		maxRows = room
	}
	rule := RuleColour(strings.Repeat(RuleFillChar(), width))
	return append([]string{rule}, m.popup.Render(width, maxRows)...)
}

// --- status row ---------------------------------------------------------

// renderStatusRow draws the bottom area's one row, in priority order:
// the modeHintText override for modeHintDuration after a Ctrl+C ("Press
// Ctrl-C again to exit", ctrl-c-hint.txt); the "Ctrl+Y to paste deleted
// text" hint for exactly one frame after a kill (docs/claude-code-reference.md
// §2: "until the next keystroke" — this replaces the old hintRow's slot
// above the input box, since the design has no row there); the verbose
// notice while Ctrl+O's detailed transcript view is active; otherwise the
// one-row status line (status.go RenderStatusLine).
func (m Model) renderStatusRow(width int) string {
	if m.modeHintText != "" {
		return FitStatus("  "+Muted(m.modeHintText), width)
	}
	if m.justKilled {
		return rightAlign(Muted("Ctrl+Y to paste deleted text"), width-2)
	}
	// A transient footer note ("mcp: connecting 2 servers…") takes this
	// row's slot until the next Update clears it (MsgFooterNote) — the
	// harness's own addition, with no Claude Code equivalent; it used to
	// share the old hint row above the input box, which the design
	// removed, so it surfaces here instead.
	if note := m.footer.Note(); note != "" {
		return FitStatus("  "+Muted(note), width)
	}
	if m.cfg.Bridge != nil && m.cfg.Bridge.Verbose() {
		// docs/claude-code-reference.md §3 (verbose-ctrl-o.txt): the status
		// row gives way to the verbose notice with "verbose" right-aligned.
		left := "  " + Muted("Showing detailed transcript · ctrl+o to toggle · ? for shortcuts")
		right := Muted("verbose")
		// One trailing column, matching verbose-ctrl-o.txt row 40 (the
		// "verbose" label ends at column 99 of 100, not flush right).
		pad := width - 1 - VisibleWidth(left) - VisibleWidth(right)
		if pad < 1 {
			return FitStatus(left, width)
		}
		return left + strings.Repeat(" ", pad) + right
	}
	return m.footer.RenderLine(width)
}

// rightAlign pads s on the left so it ends at the row's last column,
// truncating instead when it would overflow — FitStatus's own contract,
// applied after right-padding rather than before.
func rightAlign(s string, width int) string {
	w := VisibleWidth(s)
	if w >= width {
		return FitStatus(s, width)
	}
	return strings.Repeat(" ", width-w) + s
}

// The mode dot/label colour mapping now lives in status.go's modeLabel
// (RenderStatusLine's mode segment), which app.go's renderStatusRow above
// reaches through m.footer.RenderLine.
