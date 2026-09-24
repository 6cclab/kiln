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
	slashcommands "github.com/andrepato/harness/internal/commands"
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
	// MCPStatuses is every configured server's connect outcome; failures
	// become one dim transcript line after the banner.
	MCPStatuses []mcpgate.ServerStatus
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
		SessionName:    deps.Started.SessionID,
		Keymap:         bindings.Keymap(),
	}

	model := tui.NewModel(cfg)

	var opts []tea.ProgramOption
	if deps.ScreenReader {
		opts = append(opts, tea.WithColorProfile(0))
	}
	program := tea.NewProgram(model, opts...)
	bridge.SetProgram(program)

	// The startup banner, matching app.ts:1065-1068 exactly. Commit only
	// enqueues (see bridge.go's doc comment), so calling it here, before
	// program.Run(), is safe: the bridge's own committer goroutine
	// applies it once a program is attached and running.
	bridge.Commit([]string{
		tui.Green(tui.G().Call) + " " + tui.Bold("harness") + " " + tui.Dim("— "+deps.ModelLabel),
		tui.Dim("  Type / for commands, @ to reference a file, or just ask."),
	})
	if notice := mcpFailureNotice(deps.MCPStatuses); notice != "" {
		bridge.Commit([]string{tui.Dim("  " + notice)})
	}

	deps.Started.OnModelChanged = func(ctx context.Context, resolved provider.Resolved) {
		// provider/model, the same label the footer showed at startup and
		// cli.ts's onModelChanged passes.
		bridge.ModelSwitch(resolved.Model.Provider+"/"+resolved.Model.ID, resolved.Tier.Name, resolved.Tier.ContextWindow)
	}

	if status, ok := readGitStatus(ctx); ok {
		bridge.Send(tui.MsgGitStatus{Status: status})
	}

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

	_, err := program.Run()
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
func mcpFailureNotice(statuses []mcpgate.ServerStatus) string {
	var failed []mcpgate.ServerStatus
	for _, s := range statuses {
		if !s.OK {
			failed = append(failed, s)
		}
	}
	switch len(failed) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("mcp: %s unavailable (%s) · /mcp for details", failed[0].Name, failed[0].Error)
	}
	names := make([]string, len(failed))
	for i, s := range failed {
		names[i] = s.Name
	}
	return fmt.Sprintf("mcp: %d servers unavailable (%s) · /mcp for details", len(failed), strings.Join(names, ", "))
}
