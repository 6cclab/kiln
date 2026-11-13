package commands

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/auth"
	authlogin "github.com/andrepato/harness/internal/auth/login"
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
// /login and /logout run the same flow as the `kiln login` / `kiln logout`
// subcommands, inside the session, as Claude Code's own /login does: the
// flow's prompts and notices go through Interaction (the TUI draws them
// above the editor), and /login runs as a background command so Esc
// cancels it and a browser OAuth callback never blocks the event loop.
// Without an Interaction (print mode, which has nobody to prompt) they
// keep the old behaviour: report status and name the subcommand.
type AccountDeps struct {
	// Interaction supplies the in-session login prompts. nil, or a func
	// returning nil, means no interactive session.
	Interaction func() auth.Interaction
	Registry    *provider.Registry
	Todos       *agent.TodoStore
	Tier        budget.Tier
	ModelLabel  string
	// ContextUsed, if set, reports tokens currently resident.
	ContextUsed func() (int, bool)
}

func (d AccountDeps) interaction() auth.Interaction {
	if d.Interaction == nil || d.Registry == nil {
		return nil
	}
	return d.Interaction()
}

// runLogin is /login in an interactive session. With a provider it logs in
// to it; without, it asks which one first (the picker marks the ones that
// are already configured). The whole flow runs in Background: the picker,
// the browser wait and the key prompt all block on the user.
func (d AccountDeps) runLogin(wanted string, ia auth.Interaction) Result {
	if wanted != "" {
		p, ok := d.Registry.Provider(wanted)
		if !ok {
			return Result{Output: []string{fmt.Sprintf("Unknown provider %q. Providers you can log in to: %s.", wanted, strings.Join(d.loginProviderIDs(), ", "))}, Mistake: true}
		}
		if p.Auth().Kind == provider.AuthKindNone {
			return Result{Output: []string{fmt.Sprintf("%s needs no login.", wanted)}, Mistake: true}
		}
	}
	return Result{
		Background: func(ctx context.Context) (Result, error) {
			id := wanted
			if id == "" {
				choice, err := d.pickProvider(ctx, ia)
				if err != nil {
					return Result{Output: []string{"Login cancelled."}}, nil
				}
				id = choice
			}
			if err := authlogin.Login(ctx, d.Registry, d.Registry.Credentials(), id, ia); err != nil && ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			// Login reported its own outcome ("Logged in to ..." or
			// "Login failed: ...") through ia.
			return Result{}, nil
		},
		BusyLabel:  "Logging in" + d.labelSuffix(wanted),
		CancelNote: "Login cancelled.",
	}
}

func (d AccountDeps) labelSuffix(id string) string {
	if p, ok := d.Registry.Provider(id); ok {
		return " to " + p.Name()
	}
	return ""
}

// loginProviderIDs is every provider that has something to log in to.
func (d AccountDeps) loginProviderIDs() []string {
	var out []string
	for _, p := range d.Registry.Providers() {
		if p.Auth().Kind != provider.AuthKindNone {
			out = append(out, p.ID())
		}
	}
	return out
}

func (d AccountDeps) pickProvider(ctx context.Context, ia auth.Interaction) (string, error) {
	var opts []auth.SelectOption
	for _, p := range d.Registry.Providers() {
		spec := p.Auth()
		if spec.Kind == provider.AuthKindNone {
			continue
		}
		desc := "api key"
		if spec.Kind == provider.AuthKindOAuth {
			desc = "oauth"
			if spec.IsSubscription {
				desc = "subscription"
			}
		}
		if ok, err := d.Registry.CheckAuth(ctx, p.ID()); err == nil && ok {
			desc += ", configured"
		}
		opts = append(opts, auth.SelectOption{ID: p.ID(), Label: p.ID(), Description: desc})
	}
	return ia.Prompt(ctx, auth.Prompt{Type: auth.PromptSelect, Message: "Which provider do you want to log in to?", Options: opts})
}

// runLogout is /logout in an interactive session: it removes the stored
// credential straight away. A key still set in the environment keeps the
// provider usable, so say so rather than let "Logged out" mislead.
func (d AccountDeps) runLogout(ctx context.Context, id string) Result {
	if err := authlogin.Logout(ctx, d.Registry, d.Registry.Credentials(), id); err != nil {
		return Result{Output: []string{err.Error()}, Mistake: true}
	}
	lines := []string{fmt.Sprintf("Logged out of %s.", id)}
	if ok, err := d.Registry.CheckAuth(ctx, id); err == nil && ok {
		lines = append(lines, fmt.Sprintf("%s is still configured through its environment variable.", id))
	}
	return Result{Output: lines}
}

// AccountCommands returns the source for /login, /logout, /usage, /todos
// and /terminal-setup.
//
// Deliberately absent, per docs/claude-code-parity.md and
// account-commands.ts: /vim, /statusline. A command that exists only to
// say "not implemented" is noise in /help on every session. /plugin is a
// real command now — see plugin_commands.go, registered separately in
// internal/cli/commands.go (it needs the active plugin set, which
// AccountDeps doesn't carry).
func AccountCommands(deps AccountDeps) Source {
	cmds := []Command{
		{
			Name:         "login",
			Description:  "Log in to a provider (or show auth status)",
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
				if ia := deps.interaction(); ia != nil {
					return deps.runLogin(wanted, ia), nil
				}
				if wanted != "" {
					return Result{Output: []string{
						"Run this outside the session:",
						"",
						fmt.Sprintf("  kiln login %s", wanted),
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
					return Result{Output: append(append([]string{"logged in:"}, lines...), "", "Add one with: kiln login <provider>")}, nil
				}
				return Result{Output: []string{"Not logged in to any provider.", "", "Log in with: kiln login <provider>"}}, nil
			},
		},
		{
			Name:         "logout",
			Description:  "Log out of a provider",
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
					return Result{Output: []string{"Name the provider to log out of: /logout <provider>."}, Mistake: true}, nil
				}
				if deps.interaction() != nil {
					return deps.runLogout(ctx, wanted), nil
				}
				return Result{Output: []string{
					"Run this outside the session:",
					"",
					fmt.Sprintf("  kiln logout %s", wanted),
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
					"window     " + formatTokens(deps.Tier.ContextWindow) + " tokens",
					"budget     " + formatTokens(budgetTokens) + " usable after reserves",
					"tools      " + deps.Tier.ToolStrategy.Describe(),
					"per result " + formatTokens(deps.Tier.ToolOutputTokens) + " token ceiling",
				}
				if deps.ContextUsed != nil {
					if used, ok := deps.ContextUsed(); ok {
						percent := 0
						if budgetTokens > 0 {
							percent = used * 100 / budgetTokens
						}
						lines = append(lines, fmt.Sprintf("used       %s (%d%% of budget)", formatTokens(used), percent))
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
