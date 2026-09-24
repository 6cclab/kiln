package commands

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/provider"
)

// AccountDeps is everything accountCommands binds against.
//
// /usage is an adaptation, not a copy: Claude Code's version reports plan
// limits, and this harness has no plan — on a self-hosted model there is
// nothing to meter. What actually constrains a session here is the
// context budget, so that is what it reports.
//
// /login and /logout already exist as CLI subcommands. As slash commands
// they report status and point at the subcommand rather than running an
// OAuth flow inline — that flow opens a browser and binds a local port,
// which must not start mid-turn.
type AccountDeps struct {
	Registry   *provider.Registry
	Todos      *agent.TodoStore
	Tier       budget.Tier
	ModelLabel string
	// ContextUsed, if set, reports tokens currently resident.
	ContextUsed func() (int, bool)
}

// AccountCommands returns the source for /login, /logout, /usage, /todos
// and /terminal-setup.
//
// Deliberately absent, per docs/claude-code-parity.md and
// account-commands.ts: /vim, /statusline, /plugin. A command that exists
// only to say "not implemented" is noise in /help on every session.
func AccountCommands(deps AccountDeps) Source {
	cmds := []Command{
		{
			Name:         "login",
			Description:  "Show auth status, or how to log in to a provider",
			ArgumentHint: "[provider]",
			ArgumentCompletions: func(prefix string) []Completion {
				if deps.Registry == nil {
					return nil
				}
				trimmed := strings.TrimSpace(prefix)
				var out []Completion
				for _, p := range deps.Registry.Providers() {
					spec := p.Auth()
					if spec.Kind == provider.AuthKindNone || !strings.HasPrefix(p.ID(), trimmed) {
						continue
					}
					desc := "api key"
					if spec.Kind == provider.AuthKindOAuth && spec.IsSubscription {
						desc = "subscription"
					}
					out = append(out, Completion{Value: p.ID(), Label: p.ID(), Description: desc})
				}
				return out
			},
			Run: func(ctx context.Context, args string) (Result, error) {
				wanted := strings.TrimSpace(args)
				if wanted != "" {
					return Result{Output: []string{
						"Run this outside the session:",
						"",
						fmt.Sprintf("  harness login %s", wanted),
					}}, nil
				}
				if deps.Registry == nil {
					return Result{Output: []string{"Not logged in to any provider."}}, nil
				}
				var lines []string
				for _, p := range deps.Registry.Providers() {
					if p.Auth().Kind == provider.AuthKindNone {
						continue
					}
					ok, err := deps.Registry.CheckAuth(ctx, p.ID())
					if err != nil || !ok {
						continue
					}
					subscription := ""
					if p.Auth().Kind == provider.AuthKindOAuth && p.Auth().IsSubscription {
						subscription = " (subscription)"
					}
					lines = append(lines, fmt.Sprintf("  %-18s configured%s", p.ID(), subscription))
				}
				if len(lines) > 0 {
					return Result{Output: append(append([]string{"logged in:"}, lines...), "", "Add one with: harness login <provider>")}, nil
				}
				return Result{Output: []string{"Not logged in to any provider.", "", "Log in with: harness login <provider>"}}, nil
			},
		},
		{
			Name:         "logout",
			Description:  "How to log out of a provider",
			ArgumentHint: "<provider>",
			ArgumentCompletions: func(prefix string) []Completion {
				if deps.Registry == nil {
					return nil
				}
				trimmed := strings.TrimSpace(prefix)
				var out []Completion
				for _, p := range deps.Registry.Providers() {
					if !strings.HasPrefix(p.ID(), trimmed) {
						continue
					}
					ok, err := deps.Registry.CheckAuth(context.Background(), p.ID())
					if err != nil || !ok {
						continue
					}
					out = append(out, Completion{Value: p.ID(), Label: p.ID(), Description: "configured"})
				}
				return out
			},
			Run: func(ctx context.Context, args string) (Result, error) {
				wanted := strings.TrimSpace(args)
				if wanted == "" {
					return Result{Output: []string{"usage: /logout <provider>"}}, nil
				}
				return Result{Output: []string{
					"Run this outside the session:",
					"",
					fmt.Sprintf("  harness logout %s", wanted),
				}}, nil
			},
		},
		{
			Name:        "usage",
			Description: "Show context budget use for this session",
			Run: func(ctx context.Context, args string) (Result, error) {
				budgetTokens := budget.UsableTokens(deps.Tier)
				lines := []string{
					fmt.Sprintf("model      %s", deps.ModelLabel),
					fmt.Sprintf("tier       %s", deps.Tier.Name),
					fmt.Sprintf("window     %d tokens", deps.Tier.ContextWindow),
					fmt.Sprintf("budget     %d usable after reserves", budgetTokens),
					fmt.Sprintf("tools      %s", deps.Tier.ToolStrategy),
					fmt.Sprintf("per result %d token ceiling", deps.Tier.ToolOutputTokens),
				}
				if deps.ContextUsed != nil {
					if used, ok := deps.ContextUsed(); ok {
						percent := 0
						if budgetTokens > 0 {
							percent = used * 100 / budgetTokens
						}
						lines = append(lines, fmt.Sprintf("used       %d (%d%% of budget)", used, percent))
					}
				}
				lines = append(lines, "", "No plan limits apply: usage here is context, not billing.")
				return Result{Output: lines}, nil
			},
		},
		{
			Name:        "todos",
			Description: "Show the current todo list",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.Todos == nil {
					return Result{Output: []string{"No todos."}}, nil
				}
				items := deps.Todos.Get()
				if len(items) == 0 {
					return Result{Output: []string{"No todos."}}, nil
				}
				return Result{Output: renderTodos(items)}, nil
			},
		},
		{
			Name:        "terminal-setup",
			Description: "Check terminal capabilities",
			Run: func(ctx context.Context, args string) (Result, error) {
				term := envOr("TERM", "unknown")
				program := envOr("TERM_PROGRAM", "unknown")
				colors := envOr("COLORTERM", "not set")
				isTTY := isCharDevice(os.Stdout)
				lines := []string{
					fmt.Sprintf("TERM          %s", term),
					fmt.Sprintf("TERM_PROGRAM  %s", program),
					fmt.Sprintf("COLORTERM     %s", colors),
					fmt.Sprintf("tty           %s", yesNo(isTTY)),
				}
				var problems []string
				if !isTTY {
					problems = append(problems, "not a tty - the interactive TUI cannot run here")
				}
				if term == "dumb" {
					problems = append(problems, `TERM is "dumb" - use --ax-screen-reader for flat output`)
				}
				lines = append(lines, "")
				if len(problems) == 0 {
					lines = append(lines, "Terminal looks fine.")
				} else {
					for _, p := range problems {
						lines = append(lines, "- "+p)
					}
				}
				return Result{Output: lines}, nil
			},
		},
	}

	return StaticSource(OriginBuiltin, cmds)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// isCharDevice reports whether f is a terminal, without depending on
// golang.org/x/term (not vendored here): a character device is the same
// test term.IsTerminal itself makes on Unix.
func isCharDevice(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// renderTodos formats the list as "[x]/[~]/[ ] content" rows, matching
// tui/transcript.ts's renderTodos.
func renderTodos(items []agent.TodoItem) []string {
	out := make([]string, 0, len(items))
	for _, t := range items {
		mark := " "
		switch t.Status {
		case agent.TodoCompleted:
			mark = "x"
		case agent.TodoInProgress:
			mark = "~"
		}
		out = append(out, fmt.Sprintf("  [%s] %s", mark, t.Content))
	}
	return out
}
