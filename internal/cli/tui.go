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
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
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
		deps.Dispatcher.OnEvent = bridge.SubagentSink()
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

	// SIGINT and a key-driven exit converge on program.Quit; the OS signal
	// path exists for a terminal that delivers SIGINT directly rather
	// than as a Ctrl+C keypress bubbletea can see (e.g. a detached
	// controlling terminal).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)
	go func() {
		if _, ok := <-sigCh; ok {
			program.Quit()
		}
	}()

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
// (design_handoff_kiln_tui/README.md "Banner"): the KILN wordmark and
// version, the cwd/model line, then a shortcut tip row. No label rule above
// it (it is the one block the design exempts) and no "Recent sessions" list
// (no data source wired for it yet).
func bannerRows(deps InteractiveDeps) []string {
	// Version label: "v1.2.3" for a real semver, the bare string otherwise
	// (so a "dev" build reads "dev · coding agent", never "vdev").
	verLabel := Version
	if len(Version) > 0 && Version[0] >= '0' && Version[0] <= '9' {
		verLabel = "v" + Version
	}

	// Row 2 per the design: "<cwd> · branch <b> · model <m>", cwd with the
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

	// The KILN wordmark as scaled block-letter art (the design's 22px
	// wordmark; a single spaced line cannot convey that size in a
	// terminal). Amber, per the design.
	rows := make([]string, 0, len(kilnLogo)+5)
	for _, r := range kilnLogo {
		rows = append(rows, tui.KilnAmber(r))
	}
	// One blank row between rows for the design's spacing; the caller
	// (app.go) appends a full-width `─` divider after these and pins the
	// input box below.
	rows = append(rows,
		tui.Muted(verLabel+" · coding agent"),
		"",
		tui.Muted(loc),
		"",
		tips,
		"",
	)
	return rows
}

// kilnLogo is the KILN wordmark in 5-row block letters (K, I, L, N).
var kilnLogo = []string{
	"█ ▄▀  █  █    █▄ █",
	"█▀▄   █  █    █▀▄█",
	"█ ▀▄  █  █▄▄  █  █",
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
