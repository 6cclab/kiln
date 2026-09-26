// Package cli, this file: RunInteractive, the interactive TUI's entry
// point — the Go port of src/tui/app.ts's runApp, called from chat.go's
// `// phase 7:` seam.
//
// Everything runApp assembled from its own imports (the key router, the
// editor theme, mentions, history, git status) is either already built by
// this port's internal/tui package or wired here, matching how chat.go
// wires print mode's own equivalents just above the seam.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/agent"
	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/trust"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/execenv"
	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session/jsonl"
	"github.com/andrepato/harness/internal/tools"
	"github.com/andrepato/harness/internal/tui"
	"github.com/andrepato/harness/internal/tui/editor"
)

// InteractiveDeps bundles everything RunInteractive needs. Built at
// chat.go's phase 7 seam from the same local variables print mode's own
// branch uses just above it — see the seam comment there for the pieces
// that could not be wired straight through (plan approval; hook notices).
type InteractiveDeps struct {
	Cwd            string
	ModelLabel     string
	Resolved       provider.Resolved
	Started        *agent.Started
	Gate           *permission.Gate
	PlanController *agent.PlanController
	Registry       *slashcommands.Registry
	Env            *execenv.Env
	Dispatcher     *agent.Dispatcher
	HookConfig     claudehooks.Config
	SessionStart   claudehooks.Outcome
	ScreenReader   bool
	// Fullscreen selects kiln's alt-screen TUI mode (--fullscreen). Falls
	// back to inline when ScreenReader is set — see RunInteractive.
	Fullscreen bool
	// IsResume is set when this run resumed an existing session
	// (--resume/--continue): the banner's "Recent sessions" list is
	// skipped then, since the person is already inside one.
	IsResume bool
	// MCPServerCount is how many servers ConnectMCP will attempt; 0 skips
	// the connect entirely. ConnectMCP connects them all, registers their
	// tools with the session and returns every server's outcome. It runs
	// on its own goroutine after the program is up, so a slow server never
	// delays the first frame; progress reaches the footer as a note and
	// failures become one dim transcript line pointing at /mcp.
	MCPServerCount int
	ConnectMCP     func(progress func(mcpgate.ServerStatus)) []mcpgate.ServerStatus
	// Effort is the reasoning effort label shown in the banner ("medium");
	// AuthKind is how the model's provider is authenticated ("Claude
	// subscription", "API key", "Ollama").
	Effort   string
	AuthKind string
	// StatusLine is the configured status-line command (settings.json
	// "statusLine"), run to render the row below the input box. Nil = none.
	StatusLine *claudesettings.StatusLineConfig
	// LogPath is this run's diagnostics log; announced when debugging.
	LogPath string
	Debug   bool
	// Keybindings is the raw action->key map from ~/.claude/keybindings.json
	// (nil when absent). Applied to the editor once it supports overrides.
	Keybindings map[string]string
	// SetPlanApprover and SetHookNotice rebind chat.go's exit_plan_mode
	// approver and tool-guard hook notice sink to the TUI, the way cli.ts's
	// onPlanApprover/onHookNotices callbacks do. Either may be nil.
	SetPlanApprover func(tools.PlanApprover)
	SetHookNotice   func(func(string))
}

// RunInteractive drives the Bubbletea v2 program and blocks until the
// user exits (Ctrl+C twice, Ctrl+D on an empty line, or the router's
// Exit action). Session/hook cleanup (killing shells, SessionEnd,
// closing the MCP hub) intentionally stays in chat.go's Run, after this
// returns — matching cli.ts's own ordering (src/cli.ts:653-659).
func RunInteractive(ctx context.Context, deps InteractiveDeps, stdout, stderr io.Writer, stdin io.Reader) int {
	if deps.ScreenReader {
		tui.SetPlainMode(true)
	}

	bridge := tui.NewBridge(deps.Cwd)
	unwire := bridge.Wire(deps.Started, deps.Resolved.Tier.ToolOutputTokens)
	defer unwire()

	// Notification fires when the user is about to be asked for
	// permission, before the prompt paints, as Claude Code's
	// permission_prompt notification does.
	prompter := bridge.Prompter(deps.Cwd)
	deps.Gate.SetPrompter(func(ctx context.Context, req permission.Request) (permission.PromptChoice, error) {
		claudehooks.RunHooks(claudehooks.RunOptions{
			Config: deps.HookConfig,
			Event:  claudehooks.Notification,
			Payload: claudehooks.Payload{
				SessionID:        deps.Started.SessionID,
				TranscriptPath:   deps.Started.TranscriptPath,
				Cwd:              deps.Cwd,
				Message:          "Claude needs your permission to use " + req.ToolName,
				NotificationType: "permission_prompt",
			},
			OnNotice: bridge.HookNotice,
		})
		return prompter(ctx, req)
	})
	if deps.SetPlanApprover != nil {
		approve := bridge.PlanApprover()
		deps.SetPlanApprover(func(ctx context.Context, plan string) (tools.PlanDecision, error) {
			d, err := approve(ctx, plan)
			return tools.PlanDecision{Kind: tools.PlanDecisionKind(d.Kind), Mode: d.Mode, Feedback: d.Feedback}, err
		})
	}
	if deps.SetHookNotice != nil {
		deps.SetHookNotice(bridge.HookNotice)
	}
	if deps.Dispatcher != nil {
		// Fan out every dispatch event to both the transcript (a one-line
		// note per start/done/error) and the live subagents panel (which
		// tracks running/done rows for the turn in progress).
		transcriptSink := bridge.SubagentSink()
		panelSink := bridge.SubagentPanelSink()
		deps.Dispatcher.OnEvent = func(e agent.SubagentEvent) {
			transcriptSink(e)
			panelSink(e)
		}
	}

	historyPath := editor.Path()
	history := editor.Load(historyPath)

	// User keybindings over pi-tui's defaults; conflicts among the editor's
	// own actions are reported to the user as cli.ts does for the loader's.
	bindings, conflicts := tui.ApplyOverrides(tui.DefaultEditorBindings(), deps.Keybindings)
	for _, c := range conflicts {
		fmt.Fprintf(stderr, "keybindings: %s\n", c)
	}

	cfg := tui.Config{
		Cwd:            deps.Cwd,
		ModelLabel:     deps.ModelLabel,
		Tier:           deps.Resolved.Tier,
		InitialMode:    string(deps.Gate.Mode()),
		StartedAt:      time.Now(),
		Plain:          deps.ScreenReader,
		Fullscreen:     deps.Fullscreen,
		StartupContext: append([]string(nil), deps.SessionStart.Context...),

		Env:            deps.Env,
		Gate:           deps.Gate,
		PlanController: deps.PlanController,
		Lane:           deps.Started.Lane,
		Registry:       deps.Registry,
		Bridge:         bridge,

		ResolveMentions: func(ctx context.Context, line string) (string, []msg.ImageContent, []string) {
			resolved, err := ResolveMentions(line, Options{Cwd: deps.Cwd, Tier: &deps.Resolved.Tier, Roots: deps.Gate.Roots()})
			if err != nil || len(resolved.Mentions) == 0 {
				return line, nil, nil
			}
			return resolved.Prompt, resolved.Images, DescribeMentions(resolved.Mentions, deps.Cwd)
		},
		RunPromptHooks: func(ctx context.Context, line string) (string, []string) {
			outcome := claudehooks.RunHooks(claudehooks.RunOptions{
				Config: deps.HookConfig,
				Event:  claudehooks.UserPromptSubmit,
				Payload: claudehooks.Payload{
					SessionID:      deps.Started.SessionID,
					TranscriptPath: deps.Started.TranscriptPath,
					Cwd:            deps.Cwd,
					Prompt:         line,
				},
				OnNotice: bridge.HookNotice,
			})
			if outcome.Blocked != nil {
				return outcome.Blocked.Reason, nil
			}
			return "", outcome.Context
		},
		GitStatus: readGitStatus,

		HistoryPath:    historyPath,
		StartupHistory: history,
		SessionName:    "kiln", // the terminal title, as Claude Code sets "Claude Code"
		Effort:         deps.Effort,
		ModelID:        deps.Resolved.Model.ID,
		Banner:         bannerRows(deps),
		SessionID:      deps.Started.SessionID,
		TranscriptPath: deps.Started.TranscriptPath,
		Version:        Version,
		Keymap:         bindings.Keymap(),
	}
	if deps.StatusLine != nil {
		cfg.StatusLineCommand = deps.StatusLine.Command
	}

	// Folder trust, once per new folder (docs/claude-code-reference.md §5,
	// dialog-trust.txt): the harness reads the project's .claude settings
	// and hooks, so an untrusted folder is asked about before the first
	// prompt. HARNESS_TRUST_ALL=1 skips the dialog (tests, automation).
	if store, err := trust.NewStore(); err == nil && os.Getenv("HARNESS_TRUST_ALL") != "1" && !store.IsTrusted(deps.Cwd) {
		cfg.NeedsTrust = true
		cfg.OnTrust = func(trusted bool) {
			if !trusted {
				return
			}
			if err := store.Trust(deps.Cwd); err != nil {
				diag.L().Warn("trust store", "err", err)
			}
		}
	}

	// Flip the bridge's commit sink before anything commits through it (the
	// debug-log line and MCP notices below, both enqueued ahead of
	// program.Run()). Fullscreen falls back to inline under the
	// screen-reader flag — tui.NewModel makes the same check for the
	// model's own m.fullscreen, so the two stay in sync.
	if deps.Fullscreen && !deps.ScreenReader {
		bridge.SetFullscreen(true)
	}

	model := tui.NewModel(cfg)

	var opts []tea.ProgramOption
	if deps.ScreenReader {
		opts = append(opts, tea.WithColorProfile(0))
	}
	program := tea.NewProgram(model, opts...)
	bridge.SetProgram(program)

	// Finish any operation a previous process left running (see
	// agent.ResumeIncomplete's doc comment) before the program starts
	// accepting input — "the first user turn" for the TUI is whatever the
	// person types into the editor, which cannot happen before
	// program.Run() below, so this runs synchronously here instead.
	// bridge.Commit only enqueues (see its own doc comment), so this note
	// lands in the transcript right alongside the debug-log line and any
	// MCP notices, all enqueued ahead of program.Run() the same way.
	if _, err := agent.ResumeIncomplete(ctx, deps.Started, func(m string) {
		bridge.Commit([]string{tui.Muted("  " + m)})
	}); err != nil {
		diag.L().Warn("resume incomplete operation", "err", err)
	}

	// The startup banner is cfg.Banner: the app commits it on its first
	// frame, fitted to the terminal width. Commit only enqueues (see
	// bridge.go's doc comment), so the rows below, enqueued before
	// program.Run(), land right after it.
	if deps.Debug && deps.LogPath != "" {
		bridge.Commit([]string{tui.Muted("  debug log: " + deps.LogPath)})
	}
	if deps.ConnectMCP != nil && deps.MCPServerCount > 0 {
		go func() {
			n := deps.MCPServerCount
			done := 0
			bridge.Send(tui.MsgFooterNote{Text: fmt.Sprintf("mcp: connecting %d servers…", n)})
			statuses := deps.ConnectMCP(func(mcpgate.ServerStatus) {
				done++
				bridge.Send(tui.MsgFooterNote{Text: fmt.Sprintf("mcp: %d/%d servers…", done, n)})
			})
			bridge.Send(tui.MsgFooterNote{Text: ""})
			if notice := mcpFailureNotice(statuses); notice != "" {
				bridge.Commit([]string{tui.Amber("⚠") + " " + tui.Muted(notice)})
			}
		}()
	}

	deps.Started.OnModelChanged = func(ctx context.Context, resolved provider.Resolved) {
		// provider/model, the same label the footer showed at startup and
		// cli.ts's onModelChanged passes.
		bridge.ModelSwitch(resolved.Model.Provider+"/"+resolved.Model.ID, resolved.Tier.Name, resolved.Tier.ContextWindow)
	}

	// Off the main goroutine: Program.Send blocks until the event loop is
	// running, and this runs before program.Run(). Sending inline here
	// deadlocked startup in any repo whose `git rev-parse` succeeded (a
	// repo with at least one commit), which the scratch repos in tests,
	// initialised without a commit, never did.
	go func() {
		if status, ok := readGitStatus(ctx); ok {
			bridge.Send(tui.MsgGitStatus{Status: status})
		}
	}()

	// A terminal that delivers SIGINT directly rather than as a Ctrl+C
	// keypress bubbletea can see (e.g. a detached controlling terminal, or
	// a signal sent to the process directly rather than typed) does not
	// need its own handler here: bubbletea's Program already installs one
	// (tea.go's handleSignals, active unless WithoutSignalHandler is
	// passed, which this package never does) and turns that exact signal
	// into an InterruptMsg its own event loop returns from Run() as
	// ErrInterrupted.
	//
	// A second, independent signal.Notify(..., syscall.SIGINT) here used
	// to duplicate that handling — go's os/signal fans one incoming
	// signal out to every channel registered via Notify, so both this
	// package's own goroutine (calling program.Quit(), i.e. p.Send(Quit()))
	// and bubbletea's internal handleSignals() (sending InterruptMsg on
	// the same p.msgs channel) raced to send into that channel. Once
	// whichever one the event loop read first began shutdown (cancelling
	// its context and no longer draining p.msgs), the loser's send blocked
	// forever, its own handler channel never closed, and
	// channelHandlers.shutdown()'s wg.Wait() — which Program.Quit/Kill
	// both call unconditionally before anything else — hung forever.
	// Reproduced directly: a SIGINT sent to a real kiln session that had
	// run a design-sized turn (subagents, tool calls, a retry) hung on
	// exit roughly 1 run in 3 (`internal/testkit/screen`'s Exit, driving
	// testdata/drive/design-session.txt through cmd/kiln-drive); a
	// goroutine dump captured mid-hang (SIGQUIT) showed goroutine 1
	// parked in exactly that WaitGroup.Wait, called from
	// Program.shutdown via this function's line (then) 321. Removing the
	// duplicate handler removes the race.

	diag.L().Info("phase tui run", "elapsed", diag.Since())
	_, err := program.Run()
	diag.L().Info("phase tui exit", "elapsed", diag.Since(), "err", err)
	bridge.Stop()
	if err != nil {
		fmt.Fprintln(stderr, "harness:", err)
		return 1
	}
	return 0
}

// readGitStatus reads the current branch and dirty state with a 2s
// timeout, matching app.ts's readGitStatus (git.ts, not ported 1:1: this
// port runs `git rev-parse --abbrev-ref HEAD` and `git status
// --porcelain` directly rather than through a shared execenv, since it
// needs its own short timeout independent of any tool call's).
func readGitStatus(ctx context.Context) (tui.GitStatus, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return tui.GitStatus{}, false
	}
	tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	branch, err := runGit(tctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return tui.GitStatus{}, false
	}
	porcelain, err := runGit(tctx, cwd, "status", "--porcelain")
	if err != nil {
		return tui.GitStatus{}, false
	}
	return tui.GitStatus{Branch: strings.TrimSpace(branch), Dirty: strings.TrimSpace(porcelain) != ""}, true
}

func runGit(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	return string(out), err
}

// mcpFailureNotice is the one-line, dim aside for servers that did not
// connect: the session degrades to the servers that did, and /mcp has the
// details. Empty when everything connected.
// mcpFailureNotice is Claude Code's startup warning row for servers that did
// not connect (claude-code-reference.md section 1: "⚠ 1 MCP server needs
// authentication · run /mcp"). Empty when everything connected.
func mcpFailureNotice(statuses []mcpgate.ServerStatus) string {
	failed := 0
	for _, s := range statuses {
		if !s.OK {
			failed++
		}
	}
	switch failed {
	case 0:
		return ""
	case 1:
		return "1 MCP server unavailable · run /mcp"
	}
	return fmt.Sprintf("%d MCP servers unavailable · run /mcp", failed)
}

// bannerRows is the startup banner, row for row per the kiln design handoff
// (design_handoff_kiln_tui/README.md "Banner"): row 0 "K I L N  v… ·
// coding agent", row 1 "<cwd> · branch <b> · model <m>" (exactly once), row
// 2 the shortcut tips, then — unless this run resumed an existing session —
// a "Recent sessions" block listing up to 3 past sessions in this cwd. No
// label rule above it (it is the one block the design exempts).
func bannerRows(deps InteractiveDeps) []string {
	// Version label: "v1.2.3" for a real semver, the bare string otherwise
	// (so a "dev" build reads "dev · coding agent", never "vdev").
	verLabel := Version
	if len(Version) > 0 && Version[0] >= '0' && Version[0] <= '9' {
		verLabel = "v" + Version
	}

	// Row 1 per the design: "<cwd> · branch <b> · model <m>", cwd with the
	// home dir abbreviated to ~.
	loc := abbrevHome(deps.Cwd)
	if st, ok := readGitStatus(context.Background()); ok && st.Branch != "" {
		loc += " · branch " + st.Branch
	}
	loc += " · model " + deps.ModelLabel

	tips := tui.KilnAmber("/") + " " + tui.Muted("commands") + "   " +
		tui.KilnAmber("@") + " " + tui.Muted("add files") + "   " +
		tui.KilnAmber("⇧⇥") + " " + tui.Muted("cycle mode") + "   " +
		tui.KilnAmber("esc") + " " + tui.Muted("stop")

	// Row 0: "K I L N" spaced letters (the design's wordmark, one line
	// rather than the old figlet block art) amber bold, then the version
	// and tagline dim.
	rows := []string{
		tui.KilnAmber(tui.Bold("K I L N")) + "  " + tui.Muted(verLabel+" · coding agent"),
		"",
		tui.Muted(loc),
		"",
		tips,
	}

	if !deps.IsResume {
		if recent := recentSessionRows(deps.Cwd); len(recent) > 0 {
			rows = append(rows, "", tui.Muted("Recent sessions"))
			rows = append(rows, recent...)
		}
	}
	// One blank row of spacing; the caller (app.go) appends a full-width
	// `─` divider after these and pins the input box below.
	rows = append(rows, "")
	return rows
}

// recentSessionRowLimit is how many past sessions the banner lists.
const recentSessionRowLimit = 3

// recentSessionsTimeout bounds the disk read so a slow or huge session
// store never delays startup; on timeout the block is omitted silently.
const recentSessionsTimeout = 300 * time.Millisecond

// recentSessionRows renders up to recentSessionRowLimit "<when>  <title>"
// rows for the most recently modified sessions under cwd, sourced from
// internal/session/jsonl.Repo.List — the only data this run has for past
// sessions in this folder. Returns nil (silently, logged via diag) on any
// error, on timeout, or when there are no sessions.
func recentSessionRows(cwd string) []string {
	type result struct {
		rows []string
	}
	done := make(chan result, 1)
	go func() {
		rows := buildRecentSessionRows(cwd)
		done <- result{rows: rows}
	}()
	select {
	case r := <-done:
		return r.rows
	case <-time.After(recentSessionsTimeout):
		diag.L().Warn("banner: recent sessions timed out", "timeout", recentSessionsTimeout)
		return nil
	}
}

func buildRecentSessionRows(cwd string) []string {
	repo, err := jsonl.NewRepo("")
	if err != nil {
		diag.L().Warn("banner: recent sessions repo", "err", err)
		return nil
	}
	metas, err := repo.List(cwd)
	if err != nil {
		diag.L().Warn("banner: recent sessions list", "err", err)
		return nil
	}
	if len(metas) == 0 {
		return nil
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].ModifiedAt > metas[j].ModifiedAt })
	if len(metas) > recentSessionRowLimit {
		metas = metas[:recentSessionRowLimit]
	}
	now := time.Now()
	var rows []string
	for _, meta := range metas {
		title := firstUserMessageTitle(meta.Path)
		if title == "" {
			continue
		}
		when := humaneAge(now, time.UnixMilli(meta.ModifiedAt))
		rows = append(rows, "  "+tui.Muted(padTo(when, 10))+tui.Ink(title))
	}
	return rows
}

// padTo right-pads s with spaces to at least width columns (byte length —
// the "when" column is plain ASCII, so this is exact).
func padTo(s string, width int) string {
	if len(s) >= width {
		return s + " "
	}
	return s + strings.Repeat(" ", width-len(s))
}

// firstSessionReadCap bounds how much of a session's jsonl file
// firstUserMessageTitle reads looking for the first user message, so a
// huge session never slows the banner down reading to its end.
const firstSessionReadCap = 64 * 1024

// firstUserMessageTitle returns the session's first user message, single
// line, truncated to fit, or "" if it cannot find one within
// firstSessionReadCap bytes of the file. This is a cheap heuristic scan,
// not a full jsonl parse (a real parse would need session.Entry's whole
// decode+replay path, jsonl.Open, which reads to the end of the file
// regardless of where the answer sits): a user message entry encodes as
// `{"message":{"content":[{"text":"…","type":"text"}],"role":"user",…}}`
// (verified against testdata/sessions/*.jsonl), so this finds the first
// `"role":"user"` and takes the nearest preceding `"text":"..."` field —
// true for every session this repo's own writer produces, not a general
// JSON scan.
func firstUserMessageTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, firstSessionReadCap)
	n, _ := io.ReadFull(f, buf)
	data := string(buf[:n])

	idx := strings.Index(data, `"role":"user"`)
	if idx < 0 {
		return ""
	}
	textKey := `"text":"`
	ti := strings.LastIndex(data[:idx], textKey)
	if ti < 0 {
		return ""
	}
	start := ti + len(textKey)
	end := start
	for end < idx {
		if data[end] == '"' && data[end-1] != '\\' {
			break
		}
		end++
	}
	if end >= idx {
		return ""
	}
	var text string
	if err := json.Unmarshal([]byte(`"`+data[start:end]+`"`), &text); err != nil {
		text = data[start:end]
	}
	text = strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	const maxTitle = 60
	if len(text) > maxTitle {
		text = text[:maxTitle-1] + "…"
	}
	return text
}

// humaneAge renders how long ago t was, Claude-Code style: "2h ago",
// "yesterday", the weekday name within the last week, else "3d ago".
func humaneAge(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return t.Format("Mon")
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

// abbrevHome replaces the user's home directory prefix in path with "~".
func abbrevHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}
