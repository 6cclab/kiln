// Package cli, this file: the management subcommands — `harness providers`,
// `harness models`, `harness login`/`logout`, `harness doctor` and
// `harness mcp`. Ported from cli.ts's listProviders()/listModels()/login()
// and src/commands/inspect-commands.ts's /doctor and /mcp report text.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/auth"
	authlogin "github.com/andrepato/harness/internal/auth/login"
	"github.com/andrepato/harness/internal/budget"
	claudeagents "github.com/andrepato/harness/internal/claude/agents"
	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/search"
)

// subscriptionProviders returns the provider ids whose auth is a
// plan-backed subscription (auth.oauth.isSubscription in cli.ts), for the
// `harness login <a|b>` hint.
func subscriptionProviders(reg *provider.Registry) []string {
	var out []string
	for _, p := range reg.Providers() {
		if spec := p.Auth(); spec.IsSubscription {
			out = append(out, p.ID())
		}
	}
	return out
}

// Providers implements `harness providers`, matching cli.ts's
// listProviders(): every registered provider, its auth kind, and whether it
// is currently configured.
func Providers(ctx context.Context, stdout, stderr io.Writer) int {
	reg := buildRegistry()
	statuses := authlogin.Status(ctx, reg, reg.Credentials())
	subs := subscriptionProviders(reg)

	fmt.Fprintf(stdout, "%d providers\n\n", len(statuses))
	fmt.Fprintf(stdout, "  %-24s %-13s %s\n", "provider", "auth", "status")
	for _, s := range statuses {
		status := "-"
		if s.Authed {
			status = "configured"
		}
		fmt.Fprintf(stdout, "  %-24s %-13s %s\n", s.ProviderID, s.Kind, status)
	}
	if len(subs) > 0 {
		fmt.Fprintf(stdout, "\nLog in with a plan:  harness login <%s>\n", strings.Join(subs, "|"))
	}
	return 0
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Models implements `harness models [provider]`, matching cli.ts's
// listModels(): every available model, its context window, tier, tool
// strategy and usable-token budget, or (for a model whose window is too
// small to run) why it is listed as unusable rather than hidden.
func Models(ctx context.Context, stdout, stderr io.Writer, providerID string) int {
	reg := buildRegistry()

	if providerID != "" {
		p, ok := reg.Provider(providerID)
		if !ok {
			fmt.Fprintf(stderr, "harness: unknown provider %q\n", providerID)
			return 1
		}
		if err := p.RefreshModels(ctx); err != nil {
			fmt.Fprintf(stderr, "harness: refreshing %s: %v\n", providerID, err)
		}
	} else {
		for _, p := range reg.Providers() {
			_ = p.RefreshModels(ctx)
		}
	}

	available, err := reg.Available(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "harness:", err)
		return 1
	}

	type row struct {
		provider string
		model    provider.Model
	}
	var rows []row
	for _, p := range available {
		if providerID != "" && p.ID() != providerID {
			continue
		}
		for _, m := range p.Models() {
			rows = append(rows, row{provider: p.ID(), model: m})
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "No models available. Configure a provider first: harness providers")
		return 0
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].provider+rows[i].model.ID < rows[j].provider+rows[j].model.ID
	})

	fmt.Fprintf(stdout, "%d models available\n\n", len(rows))
	fmt.Fprintf(stdout, "  %-16s %-34s %9s %7s %14s %10s\n", "provider", "model", "ctx", "tier", "tools", "for convo")

	for _, r := range rows {
		resolved, err := reg.Resolve(r.provider, r.model.ID)
		if err != nil {
			var tooSmall *budget.ContextTooSmallError
			why := "unavailable"
			if errors.As(err, &tooSmall) {
				why = fmt.Sprintf("window too small (short %d)", tooSmall.Shortfall)
			}
			fmt.Fprintf(stdout, "  %-16s %-34s %9s %s\n", r.provider, truncateString(r.model.ID, 34), "-", why)
			continue
		}
		suffix := ""
		if resolved.Suppression.Suffix != "" {
			suffix = fmt.Sprintf("  [%s]", resolved.Suppression.Suffix)
		}
		fmt.Fprintf(stdout, "  %-16s %-34s %9d %7s %14s %10d%s\n",
			r.provider, truncateString(r.model.ID, 34), r.model.ContextWindow,
			resolved.Tier.Name, resolved.Tier.ToolStrategy, budget.UsableTokens(resolved.Tier), suffix)
	}
	return 0
}

// LoginCmd implements `harness login <provider>`, matching cli.ts's login():
// runs the provider's OAuth flow if it has one, else prompts for an API key
// over the terminal. Ctrl-C aborts the flow rather than the whole process.
func LoginCmd(ctx context.Context, providerID string, stdin io.Reader, stdout, stderr io.Writer) int {
	reg := buildRegistry()
	if _, ok := reg.Provider(providerID); !ok {
		fmt.Fprintf(stderr, "Unknown provider %q. See: harness providers\n", providerID)
		return 1
	}

	runCtx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	ia := auth.NewTerminalInteraction(auth.TerminalOptions{In: stdin, Out: stdout})
	if err := authlogin.Login(runCtx, reg, reg.Credentials(), providerID, ia); err != nil {
		return 1
	}
	return 0
}

// LogoutCmd implements `harness logout <provider>`.
func LogoutCmd(ctx context.Context, providerID string, stdout, stderr io.Writer) int {
	reg := buildRegistry()
	if err := authlogin.Logout(ctx, reg, reg.Credentials(), providerID); err != nil {
		fmt.Fprintln(stderr, "harness:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Logged out of %s.\n", providerID)
	return 0
}

// hookEvents mirrors inspect-commands.ts's HOOK_EVENTS, in the same order.
var hookEvents = []claudehooks.Event{
	claudehooks.PreToolUse,
	claudehooks.PostToolUse,
	claudehooks.UserPromptSubmit,
	claudehooks.SessionStart,
	claudehooks.SessionEnd,
	claudehooks.Stop,
	claudehooks.SubagentStop,
	claudehooks.Notification,
	claudehooks.PreCompact,
}

// Doctor implements `harness doctor` / the `/doctor` report from
// inspect-commands.ts: it connects to MCP for real (same as `harness mcp`)
// rather than reporting a placeholder, and reports the resident tool count
// against the resolved model's tier and tool strategy.
func Doctor(ctx context.Context, args Args, stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "harness:", err)
		return 1
	}

	settings := claudesettings.LoadSettings(cwd, claudesettings.LoadOptions{
		Sources: settingsSources(args.SettingSources),
		Extra:   args.Settings,
	})
	permissionMode := resolvePermissionMode(args, settings)

	wanted := args.Model
	if wanted == "" && strings.Contains(settings.Model, "/") {
		wanted = settings.Model
	}
	if wanted == "" {
		wanted = os.Getenv("HARNESS_MODEL")
	}
	if wanted == "" {
		wanted = defaultModel
	}

	lines := []string{fmt.Sprintf("model      %s", wanted)}

	var strategy budget.ToolStrategy
	providerID, modelID, ok := splitProviderModel(wanted)
	if ok {
		reg := buildRegistry()
		if p, ok := reg.Provider(providerID); ok {
			_ = p.RefreshModels(ctx)
		}
		resolved, err := reg.Resolve(providerID, modelID)
		if err != nil {
			lines = append(lines, fmt.Sprintf("tier       unresolvable: %v", err))
		} else {
			strategy = resolved.Tier.ToolStrategy
			lines = append(lines, fmt.Sprintf("tier       %s (%d tokens)", resolved.Tier.Name, resolved.Tier.ContextWindow))
		}
	}

	hub := mcpgate.NewHub()
	mcpConfigs := mcpgate.ResolveConfigs(args.MCPConfig, args.StrictMCPConfig)
	hub.ConnectAll(ctx, mcpConfigs)
	defer hub.Close(ctx)
	statuses := hub.Statuses()
	connected := 0
	for _, s := range statuses {
		if s.OK {
			connected++
		}
	}
	lines = append(lines, fmt.Sprintf("mcp        %d/%d connected", connected, len(statuses)))

	hasSessionSearch := false
	if s, err := search.Open(""); err == nil {
		hasSessionSearch = true
		_ = s.Close()
	}
	residentNow := residentToolNames(hasSessionSearch)
	lines = append(lines, fmt.Sprintf(`tools      %d resident, strategy "%s"`, len(residentNow), strategy))

	hookConfig := claudehooks.LoadHooks(cwd)
	hookCount := 0
	eventsWithHooks := 0
	for _, e := range hookEvents {
		n := 0
		for _, group := range hookConfig[e] {
			n += len(group.Hooks)
		}
		if n > 0 {
			eventsWithHooks++
		}
		hookCount += n
	}
	lines = append(lines, fmt.Sprintf("hooks      %d across %d events", hookCount, eventsWithHooks))

	agentsList := append([]claudeagents.Definition{agent.GeneralPurpose}, claudeagents.LoadAgents(cwd)...)
	lines = append(lines, fmt.Sprintf("agents     %d available", len(agentsList)))

	loadedFrom := make([]string, 0, len(settings.LoadedFrom))
	for _, s := range settings.LoadedFrom {
		loadedFrom = append(loadedFrom, string(s))
	}
	settingsLine := "none"
	if len(loadedFrom) > 0 {
		settingsLine = strings.Join(loadedFrom, ", ")
	}
	lines = append(lines, fmt.Sprintf("settings   %s", settingsLine))

	var problems []string
	if permissionMode == "bypassPermissions" {
		problems = append(problems, "permission mode is bypassPermissions: every tool call runs unchecked")
	}
	lines = append(lines, "")
	if len(problems) == 0 {
		lines = append(lines, "No problems found.")
	} else {
		lines = append(lines, fmt.Sprintf("%d problem(s):", len(problems)))
		for _, p := range problems {
			lines = append(lines, "  - "+p)
		}
	}

	fmt.Fprintln(stdout, strings.Join(lines, "\n"))
	return 0
}

// MCP implements `harness mcp` / the `/mcp` report from
// inspect-commands.ts: it connects to every configured server for real
// (ResolveConfigs applies --mcp-config/--strict-mcp-config the same way
// chat.go's Run does) and renders mcp.RenderMCPReport's connected/failed
// summary, rather than listing configuration only.
func MCP(ctx context.Context, args Args, stdout, stderr io.Writer) int {
	configs := mcpgate.ResolveConfigs(args.MCPConfig, args.StrictMCPConfig)
	hub := mcpgate.NewHub()
	hub.ConnectAll(ctx, configs)
	defer hub.Close(ctx)
	fmt.Fprintln(stdout, mcpgate.RenderMCPReport(hub.Statuses()))
	return 0
}

// Version implements `harness --version` / `-v`.
func Version(stdout io.Writer) int {
	fmt.Fprintln(stdout, version())
	return 0
}

// version reads the module's own version. This phase has no build-time
// version stamping, so it reports the binary name plus "dev" rather than
// inventing a number; wiring a real version (e.g. via -ldflags) is a build
// concern, not a cli one, and is left for whichever phase adds a release
// process.
func version() string {
	return "harness dev (go port)"
}
