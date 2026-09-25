package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/statusline"
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
	// ModelID is the provider/model id the verbose transcript's model row
	// shows after a turn's last tool call (docs/claude-code-reference.md
	// §3); empty falls back to ModelLabel.
	ModelID string
	// NeedsTrust opens the folder-trust dialog before the first prompt
	// (docs/claude-code-reference.md §5, dialog-trust.txt). OnTrust receives
	// the answer; "No, exit" quits the program.
	NeedsTrust bool
	OnTrust    func(trusted bool)
	// StatusLineCommand is Claude Code's settings.json "statusLine" command
	// (empty = none). It is run with the session status payload on stdin and
	// its stdout is rendered below the input box, exactly as Claude Code
	// does (docs/claude-code-reference.md §1, the "│ ⎇ …" row).
	StatusLineCommand string
	SessionID         string
	TranscriptPath    string
	Version           string
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
	dialog    Dialog
	thinking  *ThinkingView
	// popup is the `/` or `@` autocomplete list, non-nil while one of the
	// two triggers matches the editor's current line/cursor. Rebuilt from
	// scratch on every keystroke by refreshPopup — see autocomplete.go.
	popup *Popup
	// shortcuts is set while the `?` shortcuts panel is shown under the
	// input box (docs/claude-code-reference.md §6); any key closes it.
	shortcuts bool
	// bannerDone is set once Banner has been committed.
	bannerDone bool
	// committedRows counts rows printed above the live region since the last
	// clear (tea.PrintedLines). View pads the live region so the input box
	// sits at the bottom of the terminal; a constant-height live region is
	// also what keeps the renderer's cursor tracking stable across a
	// dialog/prompt opening and closing (a shrinking live region desyncs it
	// and the cursor lands above the box).
	committedRows int
	// statusLine is the rendered rows of the configured statusLine command,
	// refreshed on turn boundaries, model changes and a periodic tick.
	statusLine []string
	// lastSummary is the turn-summary lines the last finishTurn committed,
	// re-appended by a Ctrl+O replay (the summary is derived at turn end,
	// not a session entry, so replayTranscript cannot rebuild it).
	lastSummary []string
	// pendingHead is the "Name(arg)" of a mutating tool call whose header
	// row was committed when its permission prompt opened; the matching
	// result then renders without the header (ToolCallView.HeadCommitted).
	pendingHead string
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
	// viewport renders transcript, scrolled. Its own KeyMap is emptied in
	// NewModel (see there) so it never intercepts a keypress on its own;
	// scrolling is driven explicitly from handleKey/Update instead.
	viewport viewport.Model
	// resizeGen guards the debounced re-wrap a width change schedules
	// (msgFullscreenRewrap): only the most recent WindowSizeMsg's tick may
	// trigger the clear+replay, so a burst of resizes during a drag
	// rewraps once, not once per event.
	resizeGen int
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
	ed := editor.New(editor.Styles{
		Marker: marker,
		// Kiln: prompt glyph amber, rules in rule-strong (#3a3228),
		// placeholder in kiln faint (#7d7262).
		MarkerStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(hexAmber)),
		Rule:        lipgloss.NewStyle().Foreground(lipgloss.Color(hexRuleStrong)),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color(hexFaint)),
	})
	m := Model{
		cfg:            cfg,
		editor:         ed,
		footer:         NewFooterState(StatusState{ModelLabel: cfg.ModelLabel, ContextWindow: cfg.Tier.ContextWindow, Mode: cfg.InitialMode, StartedAt: cfg.StartedAt}),
		prompt:         NewPromptState(cfg.Cwd),
		subagents:      NewSubagentPanelState(),
		startupContext: append([]string(nil), cfg.StartupContext...),
		fullscreen:     cfg.Fullscreen && !cfg.Plain,
		viewport:       viewport.New(),
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
	if m.cfg.StatusLineCommand == "" {
		return m.initCmd
	}
	return tea.Batch(m.initCmd, statusLineTickCmd(), m.refreshStatusLine())
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

// toolGroup counts consecutive grouped tool calls of one kind.
type toolGroup struct {
	kind GroupKind
	n    int
}

// msgFullscreenRewrap follows a debounced width change in fullscreen mode:
// once 150ms have passed with no further WindowSizeMsg, the transcript is
// cleared and replayed at the new width (the same tea.ClearScreen +
// msgReplayTranscript sequence Ctrl+O uses), so history re-wraps instead of
// staying wrapped to a stale width. gen must match Model.resizeGen at the
// time the tick fires, or a later resize already superseded this one.
type msgFullscreenRewrap struct{ gen int }

// msgReplayTranscript follows the tea.ClearScreen a Ctrl+O toggle returns:
// once the clear has been applied, the transcript so far is re-committed at
// the new verbosity (see replayTranscript and Bridge.MsgClearAndReplay).
type msgReplayTranscript struct{}

// MsgTrustAnswered carries the trust dialog's answer; "No, exit" quits.
type MsgTrustAnswered struct{ Trusted bool }

// msgStatusLine carries the statusLine command's freshly-rendered rows.
type msgStatusLine struct{ lines []string }

// msgStatusLineTick drives the periodic statusLine refresh so time-based
// fields (the 5-hour reset clock, elapsed cost) stay current between turns.
type msgStatusLineTick struct{}

// statusLineInterval is how often the statusLine command re-runs while
// idle. Claude Code refreshes on a ~300ms render throttle; a 2s cadence
// keeps a 700-line shell script from dominating CPU while staying live.
const statusLineInterval = 2 * time.Second

func statusLineTickCmd() tea.Cmd {
	return tea.Tick(statusLineInterval, func(time.Time) tea.Msg { return msgStatusLineTick{} })
}

// refreshStatusLine runs the configured statusLine command off the Update
// loop (a tea.Cmd runs on its own goroutine) with the current session
// status, and returns its rows as msgStatusLine. It is a no-op when no
// command is configured.
func (m Model) refreshStatusLine() tea.Cmd {
	cmd := m.cfg.StatusLineCommand
	if cmd == "" {
		return nil
	}
	st := m.footer.State()
	used := 0
	if st.ContextUsed != nil {
		used = *st.ContextUsed
	}
	in := statusline.Input{
		SessionID:      m.cfg.SessionID,
		TranscriptPath: m.cfg.TranscriptPath,
		Cwd:            m.cfg.Cwd,
		Version:        m.cfg.Version,
		Model: statusline.Model{
			ID:          m.cfg.ModelID,
			DisplayName: st.ModelLabel,
		},
		Workspace: statusline.Workspace{CurrentDir: m.cfg.Cwd, ProjectDir: m.cfg.Cwd},
		ContextWindow: statusline.ContextWindow{
			ContextWindowSize: st.ContextWindow,
			TotalInputTokens:  used,
			CurrentUsage:      statusline.Usage{InputTokens: used},
		},
		Cost: statusline.Cost{TotalCostUSD: st.Cost},
	}
	if st.ContextWindow > 0 {
		in.ContextWindow.UsedPercentage = float64(used) / float64(st.ContextWindow) * 100
	}
	return func() tea.Msg {
		lines, _ := statusline.Run(context.Background(), cmd, in, 5*time.Second)
		return msgStatusLine{lines: lines}
	}
}

// msgTurnResult carries what a turn cost, once lane.Prompt returns, so
// Update can commit the turn summary/error and clear busy state. Built by
// the goroutine app.go's handleSubmit spawns, not by the bridge, since
// nothing about it is harness-event-sourced.
type msgTurnResult struct {
	result    harness.RunResult
	err       error
	toolCalls int
	seconds   int
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

	case msgFullscreenRewrap:
		if msg.gen != m.resizeGen {
			return m, nil
		}
		return m, tea.Sequence(tea.ClearScreen, func() tea.Msg { return msgReplayTranscript{} })

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
		m.editor.SetWidth(m.liveEditorWidth())
		// Kiln label rules and full-row tints size to the live content
		// width (transcript.go's width-less Render* helpers read this).
		SetRenderWidth(m.contentWidth())
		if !m.bannerDone && m.cfg.Bridge != nil && len(m.cfg.Banner) > 0 {
			rows := m.bannerRows()
			// The input box sits directly below the banner in inline mode.
			// (No bottom-pinning filler: on a tall terminal it opens a huge
			// void and, as the statusline loads and notices commit, scrolls
			// the banner off the top.) In fullscreen the banner is the
			// transcript's first content instead — appendTranscript (via
			// the bridge's fullscreen sink) puts it at the top of the
			// viewport, not scrollback.
			m.cfg.Bridge.Commit(rows)
		}
		m.bannerDone = true
		if m.fullscreen {
			m = m.layoutViewport()
			if prevWidth != 0 && prevWidth != m.width {
				m.resizeGen++
				gen := m.resizeGen
				return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return msgFullscreenRewrap{gen: gen} })
			}
		}
		return m, nil

	case tea.QuitMsg:
		m.quitting = true
		if m.cfg.Bridge != nil {
			m.cfg.Bridge.Stop()
		}
		return m, tea.Quit

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case msgSpinnerTick:
		if !m.busy {
			return m, nil
		}
		m.spinner.Tick()
		return m, tickCmd()

	case msgCommitMarkdown:
		m = m.flushGroup()
		if m.cfg.Bridge != nil {
			renderer := NewMarkdownRenderer(m.contentWidth(), IsPlain())
			lines := append([]string{""}, RenderAssistantText(renderer.Render(msg.Text))...)
			m.cfg.Bridge.Commit(lines)
		}
		return m, nil

	case msgCommitToolCall:
		if m.cfg.Bridge == nil {
			return m, nil
		}
		if kind, grouped := groupKindFor(msg.View.Name); grouped && !m.cfg.Bridge.Verbose() {
			if m.group != nil && m.group.kind != kind {
				m = m.flushGroup()
			}
			if m.group == nil {
				m.group = &toolGroup{kind: kind}
			}
			m.group.n++
			return m, nil
		}
		m = m.flushGroup()
		view := msg.View
		if m.pendingHead != "" && m.pendingHead == view.Name+"("+view.PrimaryArg+")" {
			view.HeadCommitted = true
			m.pendingHead = ""
		}
		lines := FitLines(RenderToolCall(view), m.contentWidth(), "     ")
		if !view.HeadCommitted {
			lines = append([]string{""}, lines...)
		}
		m.cfg.Bridge.Commit(lines)
		return m, nil

	case MsgRefreshMode:
		return m.refreshMode(), nil

	case MsgSubagentEvent:
		m.subagents.Apply(msg.Event)
		return m, nil

	case MsgFooterNote:
		m.footer.SetNote(msg.Text)
		return m, nil

	case msgDialogResult:
		if applier, ok := m.dialog.(interface{ Apply(msgDialogResult) }); ok {
			applier.Apply(msg)
		}
		return m, nil

	case msgReplayTranscript:
		m.replayTranscript()
		return m, nil

	case msgStatusLine:
		m.statusLine = msg.lines
		return m, nil

	case msgStatusLineTick:
		if m.cfg.StatusLineCommand == "" {
			return m, nil
		}
		return m, tea.Batch(m.refreshStatusLine(), statusLineTickCmd())

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
		return m, m.refreshStatusLine()

	case MsgGitStatus:
		g := msg.Status
		m.footer.Apply(StatusPatch{Git: &g})
		return m, nil

	case MsgPermissionPrompt:
		m = m.flushGroup()
		// A mutating call's header goes into the transcript above the
		// prompt (permission-edit.txt row 15, "⏺ Update(math.js)"); the
		// result rows follow once it has run.
		if name := strings.ToLower(msg.Request.ToolName); (name == "edit" || name == "write") && m.cfg.Bridge != nil {
			view := ToolCallView{Name: MapToolName(msg.Request.ToolName), PrimaryArg: msg.Request.PrimaryArg, Status: CallOK}
			m.pendingHead = view.Name + "(" + view.PrimaryArg + ")"
			m.cfg.Bridge.Commit(append([]string{""}, RenderToolCall(view)...))
		}
		p := m.prompt
		p.pending = &pendingPermission{request: msg.Request, reply: msg.Reply}
		p.feedback = nil
		p.plan = nil
		return m, nil

	case MsgPlanPrompt:
		p := m.prompt
		p.plan = &pendingPlan{plan: msg.Plan, reply: msg.Reply}
		p.feedback = nil
		p.pending = nil
		return m, nil

	case msgTurnResult:
		return m.finishTurn(msg), m.refreshStatusLine()

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
			lines := append([]string{""}, RenderThinking(view)...)
			if m.cfg.Bridge != nil {
				m.cfg.Bridge.Commit(lines)
			}
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

	if msg.err != nil {
		if m.cfg.Bridge != nil {
			m.cfg.Bridge.Commit(RenderError(msg.err.Error()))
		}
	} else if msg.result.Status != harness.StatusCompleted && msg.result.Status != harness.StatusAborted {
		if m.cfg.Bridge != nil {
			m.cfg.Bridge.Commit(RenderError(msg.result.Status))
		}
	}

	toolCalls := msg.toolCalls
	if m.cfg.Bridge != nil {
		toolCalls = m.cfg.Bridge.ToolCallsInTurn()
	}
	if m.cfg.Bridge != nil {
		done := time.Now()
		if t, ok := clockOverride(); ok {
			done = t
		}
		var lines []string
		if toolCalls > 0 && m.cfg.Bridge.Verbose() {
			lines = append(lines, RenderVerboseModelRow(done, m.modelID(), m.contentWidth()))
		}
		lines = append(lines, "")
		summary := RenderTurnSummary(TurnSummary{
			Seconds: msg.seconds,
			Verb:    PastTense(m.spinner.Label()),
			Done:    done,
		})
		m.lastSummary = summary
		lines = append(lines, summary...)
		m.cfg.Bridge.Commit(lines)
	}
	m.footer.SetNote("")
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
		m.cfg.Bridge.Commit(RenderUserMessage(m.dialogEcho, m.contentWidth()))
		if outcome != "" {
			m.cfg.Bridge.CommitCommandResult([]string{outcome})
		}
	}
	m.dialogEcho = ""
	return m
}

// flushGroup commits the in-flight collapsed tool-group row, if any, as
// its settled form ("  Read N files" / "  Ran N shell commands").
func (m Model) flushGroup() Model {
	if m.group == nil {
		return m
	}
	if m.cfg.Bridge != nil {
		m.cfg.Bridge.Commit([]string{"", RenderToolGroupDone(m.group.kind, m.group.n)})
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
// bottom region (spinner/dialog/prompt/editor/statusline/mode line),
// following the viewport to the bottom if it was already there before the
// resize — the same scroll-to-pause rule appendTranscript applies to new
// content applies to a height change too, since a shrinking viewport can
// otherwise leave the offset pointing past the bottom until it corrects.
func (m Model) layoutViewport() Model {
	width := m.contentWidth()
	bottom, _ := m.liveLines(width)
	h := m.height - len(bottom)
	if h < 1 {
		h = 1
	}
	wasBottom := m.viewport.AtBottom()
	m.viewport.SetWidth(width)
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

// fullscreenView composes the alt-screen frame: the transcript viewport on
// top, the same bottom region liveLines already builds (spinner/dialog/
// prompt/editor/statusline/mode line) pinned to the last rows.
func (m Model) fullscreenView() tea.View {
	if m.height <= 0 {
		// No WindowSizeMsg yet to size the viewport against; the inline
		// view degrades gracefully instead of drawing a zero-height frame.
		width := m.contentWidth()
		lines, editorTop := m.liveLines(width)
		v := tea.NewView(strings.Join(lines, "\n"))
		if m.cfg.SessionName != "" {
			v.WindowTitle = m.cfg.SessionName
		}
		if editorTop >= 0 {
			if c := m.editor.Cursor(); c != nil {
				c.Position.Y += editorTop
				v.Cursor = c
			}
		}
		return v
	}

	width := m.contentWidth()
	bottom, editorTop := m.liveLines(width)
	if len(bottom) > m.height-1 {
		overflow := len(bottom) - (m.height - 1)
		bottom = bottom[overflow:]
		if editorTop >= 0 {
			editorTop -= overflow
			if editorTop < 0 {
				editorTop = -1
			}
		}
	}

	vpRows := strings.Split(m.viewport.View(), "\n")
	// viewport.View pads to its own Height; guard the invariant explicitly
	// rather than trust it silently, since a short content string is the
	// one case that could violate it.
	for len(vpRows) < m.viewport.Height() {
		vpRows = append(vpRows, "")
	}

	content := append(append([]string{}, vpRows...), bottom...)
	v := tea.NewView(strings.Join(content, "\n"))
	if m.cfg.SessionName != "" {
		v.WindowTitle = m.cfg.SessionName
	}
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if editorTop >= 0 {
		if c := m.editor.Cursor(); c != nil {
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
			item, hasItem := m.popup.SelectedItem()
			line, _ := m.editor.CursorLine()
			if msg.String() == "enter" && hasItem && m.popup.Kind == KindSlashCommand &&
				strings.TrimSpace(line) == "/"+item.Value {
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
	)
	router := NewRouter(KeyActions{
		PermissionKey: func(msg tea.KeyPressMsg) bool {
			if !m.prompt.Active() {
				return false
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
	if consumed {
		var hintCmd tea.Cmd
		if hintMessage != "" {
			m.modeHintGen++
			gen := m.modeHintGen
			m.modeHintText = hintMessage
			hintCmd = tea.Tick(modeHintDuration, func(time.Time) tea.Msg { return msgClearModeHint{gen: gen} })
		}
		if didAbort && m.cfg.Lane != nil {
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

	width := m.contentWidth()
	echo := func() {
		if m.cfg.Bridge != nil {
			m.cfg.Bridge.Commit(RenderUserMessage(line, width))
		}
	}

	if classified, ok := ClassifyInput(line); ok {
		echo()
		return m, m.runMode(classified)
	}

	ctx := context.Background()
	var handled *commands.Result
	if m.cfg.Registry != nil {
		result, err := m.cfg.Registry.Execute(ctx, line)
		if err != nil {
			if m.cfg.Bridge != nil {
				m.cfg.Bridge.Commit(RenderError(err.Error()))
			}
			return m, nil
		}
		handled = result
	}
	if handled != nil {
		if handled.Exit {
			echo()
			return m, tea.Quit
		}
		if handled.Modal != nil {
			// Echoed when the dialog closes — see dialogEcho.
			m.dialogEcho = line
			m.dialog = NewCommandDialog(*handled.Modal)
			return m, nil
		}
		echo()
		if len(handled.Output) > 0 && m.cfg.Bridge != nil {
			m.cfg.Bridge.CommitCommandResult(handled.Output)
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
		if len(describe) > 0 && m.cfg.Bridge != nil {
			m.cfg.Bridge.Commit(append(describe, ""))
		}
	}

	var hookContext []string
	if m.cfg.RunPromptHooks != nil {
		blocked, hctx := m.cfg.RunPromptHooks(ctx, line)
		if blocked != "" {
			if m.cfg.Bridge != nil {
				m.cfg.Bridge.Commit(RenderError("blocked by hook: " + blocked))
			}
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
		return func() tea.Msg {
			lines := RunBang(context.Background(), c.Body, env)
			if bridge != nil {
				bridge.Commit(lines)
			}
			_ = cwd
			return nil
		}
	case ModeMemory:
		lines := AddMemory(c.Body, m.cfg.Cwd)
		if m.cfg.Bridge != nil {
			m.cfg.Bridge.Commit(lines)
		}
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
	// A fresh turn starts with no dispatches: the previous turn's
	// subagents panel (if any) does not linger into this one.
	m.subagents.Reset()

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

func (m Model) contentWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
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
	rows := make([]string, len(m.cfg.Banner))
	for i, r := range m.cfg.Banner {
		rows[i] = FitStatus(r, m.contentWidth())
	}
	ruleCh := "─"
	if IsPlain() {
		ruleCh = "-"
	}
	rows = append(rows, Rule(strings.Repeat(ruleCh, m.contentWidth())))
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
	lines, editorTop := m.liveLines(width)

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
			c.Position.Y += editorTop
			v.Cursor = c
		}
	}
	return v
}

// liveLines builds the live-region rows (everything below the committed
// transcript: spinner, dialog/prompt, input box, statusLine, mode line) and
// the index of the editor's first row (editorTop, -1 when the editor is not
// shown). Split out of View so WindowSizeMsg can measure the live region's
// height to size the startup filler.
func (m Model) liveLines(width int) (lines []string, editorTop int) {
	editorTop = -1

	if m.group != nil {
		lines = append(lines, "", RenderToolGroupRunning(m.group.kind, m.group.n))
	}
	// No spinner while a permission or plan prompt is up: Claude Code shows
	// the question alone (permission-edit.txt, plan-approval.txt).
	if !m.prompt.Active() {
		if s := m.spinner.Render(width, time.Time{}); len(s) > 0 {
			lines = append(lines, s...)
		}
	}
	if m.thinking != nil {
		view := *m.thinking
		view.Expanded = false
		if collapsed := RenderThinking(view); len(collapsed) > 0 {
			lines = append(lines, FitStatus(collapsed[0], width))
		}
	}
	switch {
	case m.dialog != nil:
		// A dialog replaces the input box and mode line under a `▔` rule
		// carrying the effort indicator (docs/claude-code-reference.md §5).
		lines = append(lines, m.dialogRows(width, len(lines))...)
	case m.prompt.Active():
		// Permission and plan prompts render inline in place of the input
		// box, with no hint row or mode line (§5, permission-edit.txt). Plan
		// approval sits under a full `▔` rule and a blank row
		// (plan-approval.txt rows 3-4).
		if m.prompt.plan != nil {
			lines = append(lines, Rule(strings.Repeat("▔", width)), "")
		}
		lines = append(lines, m.prompt.Render(width)...)
	case m.cfg.Bridge != nil && m.cfg.Bridge.Verbose():
		// The detailed transcript view (verbose-ctrl-o.txt rows 38-39): a
		// rule and the notice row take the input box's place until Ctrl+O
		// toggles back.
		lines = append(lines, RuleColour(strings.Repeat("─", width)), m.renderModeLine(width))
	default:
		if m.popup != nil {
			// Claude Code draws the suggestions directly above the input
			// box's top rule (autocomplete-slash.txt rows 29-32).
			lines = append(lines, m.renderPopup(width, len(lines))...)
		}
		// The subagents panel sits above the hint row/input box, live for
		// the turn that dispatched at least one `task` call; empty
		// otherwise, so it costs no rows when nothing is running.
		if rows := m.subagents.Render(width); len(rows) > 0 {
			lines = append(lines, rows...)
			lines = append(lines, "")
		}
		if hint := m.hintRow(width); hint != "" {
			lines = append(lines, hint)
		}
		editorTop = len(lines)
		lines = append(lines, m.editor.View(width)...)
		if m.shortcuts {
			// The shortcuts panel takes the mode line's place
			// (shortcuts.txt rows 32-39).
			lines = append(lines, RenderShortcuts(width)...)
		} else {
			// The configured statusLine sits between the input box and the
			// mode line (docs/claude-code-reference.md §1: the "│ ⎇ …" row).
			// The statusLine command emits its own (non-kiln) colours, so
			// strip them and re-tint the row in the kiln dim tone: it keeps
			// its text/segments and layout, just in the kiln palette.
			for _, sl := range m.statusLine {
				lines = append(lines, FitStatus("  "+Muted(ansi.Strip(sl)), width))
			}
			lines = append(lines, m.renderModeLine(width))
		}
	}

	return lines, editorTop
}

// dialogRows renders the open dialog under its `▔` rule, sized to what is
// left of the terminal below linesAbove.
func (m Model) dialogRows(width, linesAbove int) []string {
	height := m.frameHeight()
	room := height - linesAbove - 1
	if room < 4 {
		room = 4
	}
	rows := []string{m.dialogRule(width)}
	for _, r := range m.dialog.Render(width, room) {
		rows = append(rows, FitStatus(r, width))
	}
	return rows
}

// dialogRule is the `▔` rule above a dialog with the effort indicator set
// in near the right edge: `▔…▔ ◐ medium · /effort ▔` (dialog-model.txt row 24).
func (m Model) dialogRule(width int) string {
	effort := m.cfg.Effort
	if effort == "" {
		effort = "medium"
	}
	label := " " + effortGlyph(effort) + " " + effort + " · /effort "
	lead := width - VisibleWidth(label) - 1
	if lead < 0 {
		return FitStatus(Muted(strings.TrimSpace(label)), width)
	}
	// Kiln restyle: the ▔ rule uses the kiln hairline colour rather than
	// the Claude Code accent.
	return Rule(strings.Repeat("▔", lead)) + Muted(label) + Rule("▔")
}

// modelID is the id the verbose model row shows.
func (m Model) modelID() string {
	if m.cfg.ModelID != "" {
		return m.cfg.ModelID
	}
	return m.footer.State().ModelLabel
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

// replayTranscript re-commits every entry on the lane's current branch,
// rendered at the current width and verbosity. Committed rows live in
// scrollback and cannot be repainted, so a verbosity change redraws from
// the session log, the same source Claude Code redraws from.
//
// In fullscreen the banner needs the same treatment: it is part of the
// transcript buffer (not native scrollback), so a clear wipes it along with
// everything else, and it must be re-committed first — ahead of the
// entries — or Ctrl+O/toggle/resize-rewrap would each drop it.
func (m Model) replayTranscript() {
	if m.cfg.Bridge == nil || m.cfg.Lane == nil {
		return
	}
	if m.fullscreen && m.bannerDone && len(m.cfg.Banner) > 0 {
		m.cfg.Bridge.Commit(m.bannerRows())
	}
	entries, err := m.cfg.Lane.FindEntries(context.Background())
	if err != nil {
		m.cfg.Bridge.Commit(RenderError(err.Error()))
		return
	}
	m.cfg.Bridge.Commit(RenderTranscriptEntries(oldestFirst(entries), m.contentWidth(), m.cfg.Bridge.Verbose(), m.cfg.Cwd, m.lastSummary))
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
	bridge := m.cfg.Bridge
	m.dialog = NewRewindDialog(RewindEntriesFromSession(entries), func(entryID string) error {
		if err := lane.NavigateTree(context.Background(), parentOf(entries, entryID)); err != nil {
			return err
		}
		if bridge != nil {
			bridge.Send(MsgClearAndReplay{})
		}
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
	room := height - linesAbove - footerRows
	if room < 1 {
		return nil
	}
	maxRows := 5
	if maxRows > room {
		maxRows = room
	}
	return m.popup.Render(width, maxRows)
}

// --- hint row and mode line -------------------------------------------------

// hintRow renders the right-aligned row above the input box's top rule:
// "Ctrl+Y to paste deleted text" for exactly one frame after a kill,
// otherwise the effort indicator "◐ medium · /effort"
// (docs/claude-code-reference.md §2). Empty while a permission/plan prompt
// or the autocomplete popup owns the area below — "the hint row is
// omitted" per the task's explicit instruction — matching startup.txt's
// own layout where the effort row sits directly above the rule with
// nothing else competing for it.
func (m Model) hintRow(width int) string {
	if m.prompt.Active() || m.popup != nil {
		return ""
	}
	// Two trailing columns after the indicator (startup.txt row 26 ends at
	// column 98 of 100).
	if m.justKilled {
		return rightAlign(Muted("Ctrl+Y to paste deleted text"), width-2)
	}
	effort := m.cfg.Effort
	if effort == "" {
		effort = "medium"
	}
	// Kiln restyle: the effort glyph+label is the accent (amber), the
	// "· /effort" hint stays dim.
	right := KilnAmber(effortGlyph(effort)+" "+effort) + Muted(" · /effort")
	// A transient footer note ("mcp: connecting 11 servers…") takes the
	// left of the same row; the harness's own addition, Claude Code has no
	// equivalent row.
	left := ""
	if note := m.footer.Note(); note != "" {
		left = "  " + Muted(note)
	}
	pad := width - 2 - VisibleWidth(left) - VisibleWidth(right)
	if pad < 1 {
		return FitStatus(left, width)
	}
	return left + strings.Repeat(" ", pad) + right
}

// effortGlyph maps a reasoning-effort label to its indicator glyph
// (docs/claude-code-reference.md §1: "Glyph by effort: `◔ low`, `◐
// medium`, `◕ high`, `● xhigh/max`"). Unrecognized labels fall back to the
// medium glyph rather than an empty one, since the row must always show
// something.
func effortGlyph(effort string) string {
	switch effort {
	case "low":
		return "◔"
	case "high":
		return "◕"
	case "xhigh", "max":
		return "●"
	default:
		return "◐"
	}
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

// modeLineText returns the mode line's lead-in glyph and remaining text
// for mode, verbatim from docs/claude-code-reference.md §2 (verified
// against mode-cycle.txt): a non-cycling mode still gets the glyph, just
// no "(shift+tab to cycle)" suffix.
func modeLineText(mode string) (glyph, rest string, ok bool) {
	switch mode {
	case "auto":
		return "⏵⏵", "auto mode on (shift+tab to cycle)", true
	case "manual":
		return "⏸", "manual mode on", true
	case "acceptEdits":
		return "⏵⏵", "accept edits on (shift+tab to cycle)", true
	case "plan":
		return "⏸", "plan mode on (shift+tab to cycle)", true
	case "bypassPermissions":
		return "⏵⏵", "bypass permissions on (shift+tab to cycle)", true
	case "dontAsk":
		return "⏵⏵", "don't ask on (shift+tab to cycle)", true
	default:
		return "", "", false
	}
}

// renderModeLine draws the bottom area's one row: the mode line, indented
// two spaces, glyph in Amber and the rest in Muted — or, for
// modeHintDuration after a Ctrl+C, "Press Ctrl-C again to exit" in its
// place (docs/claude-code-reference.md §2, ctrl-c-hint.txt). " · ← for
// agents" follows the auto and manual modes only (mode-cycle.txt: accept
// edits and plan never carry it; turn-edit.txt row 40 carries it with text
// in the input) and is dropped while the autocomplete popup is open
// (autocomplete-slash.txt row 36). The harness has no agents view yet, so
// the suffix is parity-only and Left on an empty input does nothing.
func (m Model) renderModeLine(width int) string {
	if m.modeHintText != "" {
		return FitStatus("  "+Muted(m.modeHintText), width)
	}
	if m.cfg.Bridge != nil && m.cfg.Bridge.Verbose() {
		// docs/claude-code-reference.md §3 (verbose-ctrl-o.txt): the mode
		// line gives way to the verbose notice with "verbose" right-aligned.
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
	_, rest, ok := modeLineText(m.footer.State().Mode)
	if !ok {
		return FitStatus("", width)
	}
	mode := m.footer.State().Mode
	if (mode == "auto" || mode == "manual") && m.popup == nil {
		rest += " · ← for agents"
	}
	// Kiln restyle: a filled dot "●" leads the line instead of the
	// Claude Code ⏵⏵/⏸ glyph, coloured by mode (ask/manual dim, the
	// auto-edit family green, plan blue); the wording itself is
	// unchanged. See design_handoff_kiln_tui/README.md's status-line
	// mode colours.
	return FitStatus("  "+modeDotColor(mode)("●")+" "+Muted(rest), width)
}

// modeDotColor picks the kiln colour for the mode line's lead-in "●",
// mapping the harness's permission-mode names onto the design's three
// mode colours: ask/manual (dim), the auto-edit family — auto,
// acceptEdits, bypassPermissions, dontAsk — (green), and plan (blue).
func modeDotColor(mode string) func(string) string {
	switch mode {
	case "plan":
		return KilnBlue
	case "auto", "acceptEdits", "bypassPermissions", "dontAsk":
		return KilnGreen
	default:
		return Muted
	}
}
