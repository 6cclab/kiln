package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tui/editor"
)

// SPINNER_INTERVAL_MS, matching app.ts's own constant.
const spinnerInterval = 80 * time.Millisecond

// permissionModes cycles Shift+Tab, matching app.ts's MODES.
var permissionModes = []string{"manual", "acceptEdits", "auto", "plan"}

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
	Cwd            string
	ModelLabel     string
	Tier           budget.Tier
	InitialMode    string
	StartedAt      time.Time
	Plain          bool
	StartupContext []string

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
}

// Model is the interactive shell's Bubbletea v2 model — the Go port of
// app.ts's runApp, restructured around Update/View instead of runApp's
// direct pi-tui component tree. See doc.go for the mapping.
type Model struct {
	cfg Config

	width, height int

	editor   editor.Model
	spinner  SpinnerState
	footer   *FooterState
	prompt   *PromptState
	modal    *ModalView
	thinking *ThinkingView
	// popup is the `/` or `@` autocomplete list, non-nil while one of the
	// two triggers matches the editor's current line/cursor. Rebuilt from
	// scratch on every keystroke by refreshPopup — see autocomplete.go.
	popup *Popup
	// transcriptView is set while the Ctrl+R alt-screen transcript view is
	// open. See transcriptview.go.
	transcriptView *TranscriptView

	busy          bool
	turn          int
	turnStartedAt time.Time

	// hint is a one-shot status-line note the router's actions set (e.g.
	// "press ctrl+c again to exit", "nothing truncated"), cleared by
	// refreshStatus the way app.ts's refreshStatus(note = "") does.
	quitting bool

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
	marker := G().UserMark
	ed := editor.New(editor.Styles{
		Marker:      marker,
		Rule:        lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Placeholder: lipgloss.NewStyle().Faint(true),
	})
	m := Model{
		cfg:            cfg,
		editor:         ed,
		footer:         NewFooterState(StatusState{ModelLabel: cfg.ModelLabel, ContextWindow: cfg.Tier.ContextWindow, Mode: cfg.InitialMode, StartedAt: cfg.StartedAt}),
		prompt:         NewPromptState(cfg.Cwd),
		startupContext: append([]string(nil), cfg.StartupContext...),
	}
	if len(cfg.StartupHistory) > 0 {
		m.editor.SetHistory(cfg.StartupHistory)
	}
	m.initCmd = m.editor.Focus()
	return m
}

func (m Model) Init() tea.Cmd {
	return m.initCmd
}

// --- messages owned by app.go itself ------------------------------------

type msgSpinnerTick struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return msgSpinnerTick{} })
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

func (m Model) Update(tm tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := tm.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.editor.SetWidth(m.liveEditorWidth())
		if m.transcriptView != nil {
			m.transcriptView.vp.SetWidth(m.contentWidth())
			m.transcriptView.vp.SetHeight(m.transcriptViewportHeight())
		}
		return m, nil

	case msgAltScreenAppend:
		if m.transcriptView != nil {
			m.transcriptView.Append(msg.Text)
		}
		return m, nil

	case tea.MouseWheelMsg:
		if m.transcriptView != nil {
			return m.handleTranscriptViewMouse(msg)
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
		if m.cfg.Bridge != nil {
			renderer := NewMarkdownRenderer(m.contentWidth(), IsPlain())
			lines := append([]string{""}, renderer.Render(msg.Text)...)
			m.cfg.Bridge.Commit(lines)
		}
		return m, nil

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

	case MsgPermissionPrompt:
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
		return m.finishTurn(msg), nil
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
		lines := append([]string{}, RenderTurnSummary(TurnSummary{
			Seconds:   msg.seconds,
			Tokens:    m.spinner.Tokens(),
			ToolCalls: toolCalls,
		})...)
		lines = append(lines, "")
		m.cfg.Bridge.Commit(lines)
	}
	m.footer.SetNote("")
	return m
}

// --- key handling ----------------------------------------------------------

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.transcriptView != nil {
		return m.handleTranscriptViewKey(msg)
	}

	if m.modal != nil {
		consumed, shouldClose := m.modal.HandleKey(msg)
		if shouldClose {
			m.modal = nil
			return m, nil
		}
		if consumed {
			return m, nil
		}
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
			replacement, _ := m.popup.Accept()
			m.editor.ReplaceCursorLine(m.popup.Start, m.popup.End, replacement)
			m.popup = nil
			m.editor.PopupActive = false
			return m, nil
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
		didAbort    bool
		didClear    bool
		didClearScr bool
		didExit     bool
		didCycle    bool
		didToggle   bool
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
		Rewind: func() {
			if m.cfg.Bridge != nil {
				m.cfg.Bridge.Commit([]string{Dim("  rewind: use /rewind <entry-id>; /resume lists past sessions")})
			}
		},
		Exit: func() { didExit = true },
		Hint: func(message string) { m.footer.SetNote(message) },
	})

	router.lastCtrlC = m.lastCtrlC
	router.lastEsc = m.lastEsc
	consumed := router.Route(msg)
	m.lastCtrlC = router.lastCtrlC
	m.lastEsc = router.lastEsc
	if consumed {
		if didAbort && m.cfg.Lane != nil {
			_ = m.cfg.Lane.Abort()
		}
		if didClear {
			m.editor.SetValue("")
		}
		if didCycle {
			m = m.cycleMode()
		}
		if didToggle {
			m = m.openTranscriptView()
		}
		if didExit {
			return m, tea.Quit
		}
		if didClearScr {
			return m, tea.ClearScreen
		}
		m = m.refreshPopup()
		return m, nil
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

func (m Model) cycleMode() Model {
	if m.cfg.Gate == nil {
		return m
	}
	cur := string(m.cfg.Gate.Mode())
	idx := 0
	for i, mode := range permissionModes {
		if mode == cur {
			idx = i
			break
		}
	}
	next := permissionModes[(idx+1)%len(permissionModes)]
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
	if m.cfg.Bridge != nil {
		echo := append([]string{""}, RenderUserMessage(line, width)...)
		echo = append(echo, "")
		m.cfg.Bridge.Commit(echo)
	}

	if classified, ok := ClassifyInput(line); ok {
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
		if handled.Modal != nil {
			m.modal = NewModalView(*handled.Modal)
			return m, nil
		}
		if len(handled.Output) > 0 && m.cfg.Bridge != nil {
			m.cfg.Bridge.Commit(handled.Output)
		}
		if handled.Prompt == "" {
			return m, nil
		}
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
	m.turn++
	m.turnStartedAt = time.Now()
	m.spinner.Start(m.turn)
	m.footer.SetBusy(true)

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

// View composes the live region: spinner, live thinking, permission/plan
// prompt, the editor frame, then the 2-row footer — app.ts's own layout
// order (transcript above; spinner, prompt, input box, status line, in
// that order, below). Everything here is redrawn every frame; the
// committed transcript lives in real scrollback and is never touched.
func (m Model) View() tea.View {
	if m.transcriptView != nil {
		return m.transcriptViewView()
	}

	width := m.contentWidth()
	var lines []string

	if s := m.spinner.Render(width, time.Time{}); len(s) > 0 {
		lines = append(lines, s...)
	}
	if m.thinking != nil {
		view := *m.thinking
		view.Expanded = false
		if collapsed := RenderThinking(view); len(collapsed) > 0 {
			lines = append(lines, FitStatus(collapsed[0], width))
		}
	}
	if m.prompt.Active() {
		lines = append(lines, m.prompt.Render(width)...)
	}
	lines = append(lines, m.editor.View(width)...)
	if m.popup != nil {
		lines = append(lines, m.renderPopup(width, len(lines))...)
	}
	footerRows := m.footer.Render(width)
	lines = append(lines, footerRows[0], footerRows[1])

	if m.modal != nil {
		modalWidth := width * 8 / 10
		if modalWidth < 20 {
			modalWidth = width
		}
		modalHeight := len(lines) * 8 / 10
		if modalHeight < 6 {
			modalHeight = min(6, len(lines))
		}
		overlay := m.modal.Render(modalWidth, modalHeight)
		lines = compositeCenter(lines, overlay, width)
	}

	v := tea.NewView(strings.Join(lines, "\n"))
	if m.cfg.SessionName != "" {
		v.WindowTitle = m.cfg.SessionName
	}
	if !m.prompt.Active() && m.modal == nil {
		if c := m.editorCursor(); c != nil {
			v.Cursor = c
		}
	}
	return v
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
	height := m.height
	if height <= 0 {
		height = 24
	}
	const footerRows = 2
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

// editorCursor offsets the editor's own cursor by the number of live-
// region rows drawn above it (spinner, thinking, prompt).
func (m Model) editorCursor() *tea.Cursor {
	c := m.editor.Cursor()
	if c == nil {
		return nil
	}
	width := m.contentWidth()
	above := 0
	above += len(m.spinner.Render(width, time.Time{}))
	if m.thinking != nil {
		above++
	}
	c.Position.Y += above
	return c
}

// compositeCenter splices overlay over base, centered, matching modal.ts's
// showOverlay({width: "80%", maxHeight: "80%", anchor: "center"}) — a
// panel that takes the keyboard should look like it is floating over the
// conversation it is asking about, not replacing it outright.
func compositeCenter(base, overlay []string, width int) []string {
	if len(overlay) == 0 {
		return base
	}
	rows := make([]string, len(base))
	copy(rows, base)
	if len(overlay) > len(rows) {
		rows = append(rows, make([]string, len(overlay)-len(rows))...)
	}
	top := (len(rows) - len(overlay)) / 2
	if top < 0 {
		top = 0
	}
	overlayWidth := 0
	for _, l := range overlay {
		if w := VisibleWidth(l); w > overlayWidth {
			overlayWidth = w
		}
	}
	left := (width - overlayWidth) / 2
	if left < 0 {
		left = 0
	}
	pad := strings.Repeat(" ", left)
	for i, l := range overlay {
		rows[top+i] = pad + l
	}
	return rows
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
