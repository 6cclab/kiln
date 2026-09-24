// Package cli, this file: the management subcommands — `harness providers`,
// `harness models`, `harness login`/`logout`, `harness doctor` and
// `harness mcp`. Ported from cli.ts's listProviders()/listModels()/login()
// and src/commands/inspect-commands.ts's /doctor and /mcp report text.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/auth"
	authlogin "github.com/andrepato/harness/internal/auth/login"
	"github.com/andrepato/harness/internal/budget"
	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/claude/paths"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/provider"
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
// inspect-commands.ts, adapted to this phase's scope: no MCP hub and no
// subagent roster exist yet, so those two lines say so plainly rather than
// reporting a fake zero.
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
			lines = append(lines, fmt.Sprintf("tier       %s (%d tokens)", resolved.Tier.Name, resolved.Tier.ContextWindow))
			lines = append(lines, fmt.Sprintf(`tools      4 resident, strategy "%s"`, resolved.Tier.ToolStrategy))
		}
	}

	// phase 5: MCP hub status belongs here once it exists.
	lines = append(lines, "mcp        not connected yet (phase 5)")

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

	// phase 6: the subagent roster (.claude/agents plus the built-in
	// general-purpose agent) belongs here once the task tool exists.
	lines = append(lines, "agents     not implemented yet (phase 6)")

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

// mcpServerConfig mirrors mcp/client.ts's McpServerConfig, read from
// ~/.claude.json's "mcpServers" map.
type mcpServerConfig struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func readMCPServerConfigs(path string) map[string]mcpServerConfig {
	if path == "" {
		path = paths.ClaudeJSONPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		MCPServers map[string]mcpServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return cfg.MCPServers
}

// MCP implements `harness mcp` / the `/mcp` report from
// inspect-commands.ts, adapted to this phase's scope: no MCP hub connects
// yet, so servers are listed by configuration only, with their status
// explicitly deferred rather than faked as connected or failed.
func MCP(ctx context.Context, mcpConfigPath string, stdout, stderr io.Writer) int {
	servers := readMCPServerConfigs(mcpConfigPath)
	if len(servers) == 0 {
		fmt.Fprintln(stdout, "No MCP servers configured. They are read from ~/.claude.json")
		return 0
	}

	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Fprintf(stdout, "%d server(s) configured. Connection status arrives in phase 5.\n\n", len(names))
	for _, name := range names {
		cfg := servers[name]
		switch {
		case cfg.URL != "":
			fmt.Fprintf(stdout, "  %-22s url      %s\n", name, cfg.URL)
		case cfg.Command != "":
			fmt.Fprintf(stdout, "  %-22s command  %s %s\n", name, cfg.Command, strings.Join(cfg.Args, " "))
		default:
			fmt.Fprintf(stdout, "  %-22s (no command or url configured)\n", name)
		}
	}
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
