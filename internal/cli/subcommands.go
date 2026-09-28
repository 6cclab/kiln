// Package cli, this file: the management subcommands — `harness providers`,
// `harness models`, `harness login`/`logout`, `harness doctor` and
// `harness mcp`. Ported from cli.ts's listProviders()/listModels()/login()
// and src/commands/inspect-commands.ts's /doctor and /mcp report text.
package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/plural"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
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
		fmt.Fprintf(stdout, "\nLog in with a plan:  kiln login <%s>\n", strings.Join(subs, "|"))
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
			fmt.Fprintf(stderr, "kiln: unknown provider %q\n", providerID)
			return 1
		}
		if err := p.RefreshModels(ctx); err != nil {
			fmt.Fprintf(stderr, "kiln: refreshing %s: %v\n", providerID, err)
		}
	} else {
		for _, p := range reg.Providers() {
			_ = p.RefreshModels(ctx)
		}
	}

	available, err := reg.Available(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "kiln:", err)
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
		fmt.Fprintln(stdout, "No models available. Configure a provider first: kiln providers")
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

	// Roles are printed as a second table, only when any are configured -
	// most sessions have none, and an empty table would just be noise
	// after the model listing.
	if cwd, err := os.Getwd(); err == nil {
		settings := claudesettings.LoadSettings(cwd, claudesettings.LoadOptions{})
		if len(settings.ModelRoles) > 0 {
			refreshRoleProviders(ctx, reg, settings.ModelRoles, providerID, nil)
			var all []provider.Model
			for _, p := range available {
				for _, m := range p.Models() {
					all = append(all, provider.Model{ID: m.ID, Provider: p.ID(), ContextWindow: m.ContextWindow})
				}
			}
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, "roles")
			for _, line := range renderModelRolesTable(settings.ModelRoles, all) {
				fmt.Fprintln(stdout, "  "+line)
			}
		}
	}
	return 0
}

// renderModelRolesTable renders settings.json's modelRoles: one line per
// role with its provider/model value, tier and usable-token budget, or
// "(unresolved: ...)" when agents.ValidateRoles flags that role's value.
// Mirrors internal/commands' modelRolesTable (/model roles); duplicated
// rather than shared because the two packages use the value for different
// surrounding output (a slash-command Result vs. a plain stdout report) and
// internal/commands cannot import internal/cli.
func renderModelRolesTable(roles map[string]string, models []provider.Model) []string {
	byID := map[string]provider.Model{}
	candidates := make([]claudeagents.Candidate, 0, len(models))
	for _, m := range models {
		byID[m.Provider+"/"+m.ID] = m
		candidates = append(candidates, claudeagents.Candidate{ID: m.ID, Provider: m.Provider})
	}
	problems := map[string]string{}
	for _, p := range claudeagents.ValidateRoles(roles, candidates) {
		if idx := strings.Index(p, ": "); idx != -1 {
			problems[p[:idx]] = p[idx+2:]
		}
	}

	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)

	lines := make([]string, 0, len(names)+1)
	lines = append(lines, fmt.Sprintf("%-12s %-30s %8s %10s", "role", "provider/model", "tier", "usable"))
	for _, name := range names {
		value := roles[name]
		if reason, bad := problems[name]; bad {
			lines = append(lines, fmt.Sprintf("%-12s %-30s (unresolved: %s)", name, value, reason))
			continue
		}
		tier := budget.TierFor(byID[value].ContextWindow)
		lines = append(lines, fmt.Sprintf("%-12s %-30s %8s %10d", name, value, tier.Name, budget.UsableTokens(tier)))
	}
	return lines
}

// LoginCmd implements `harness login <provider>`, matching cli.ts's login():
// runs the provider's OAuth flow if it has one, else prompts for an API key
// over the terminal. Ctrl-C aborts the flow rather than the whole process.
func LoginCmd(ctx context.Context, providerID string, stdin io.Reader, stdout, stderr io.Writer) int {
	reg := buildRegistry()
	if _, ok := reg.Provider(providerID); !ok {
		fmt.Fprintf(stderr, "Unknown provider %q. See: kiln providers\n", providerID)
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
		fmt.Fprintln(stderr, "kiln:", err)
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
		fmt.Fprintln(stderr, "kiln:", err)
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
	var problems []string

	reg := buildRegistry()
	var strategy budget.ToolStrategy
	providerID, modelID, ok := splitProviderModel(wanted)
	if ok {
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

	if len(settings.ModelRoles) > 0 {
		refreshRoleProviders(ctx, reg, settings.ModelRoles, providerID, nil)
		if available, err := reg.Available(ctx); err == nil {
			var candidates []claudeagents.Candidate
			var models []provider.Model
			for _, p := range available {
				for _, m := range p.Models() {
					candidates = append(candidates, claudeagents.Candidate{ID: m.ID, Provider: p.ID()})
					models = append(models, provider.Model{ID: m.ID, Provider: p.ID(), ContextWindow: m.ContextWindow})
				}
			}
			problems = append(problems, claudeagents.ValidateRoles(settings.ModelRoles, candidates)...)

			names := make([]string, 0, len(settings.ModelRoles))
			for name := range settings.ModelRoles {
				names = append(names, name)
			}
			sort.Strings(names)
			lines = append(lines, fmt.Sprintf("roles      %d configured", len(names)))
			for _, line := range renderModelRolesTable(settings.ModelRoles, models) {
				lines = append(lines, "             "+line)
			}

			// Two roles that both point at ollama but name different
			// models: ollama typically keeps one model resident at a
			// time, so a second role loading a second model can stall
			// or evict the first mid-session.
			ollamaModelByRole := map[string]string{}
			for _, name := range names {
				if pID, mID, ok := splitProviderModel(settings.ModelRoles[name]); ok && pID == "ollama" {
					ollamaModelByRole[name] = mID
				}
			}
			roleNamesWithOllama := make([]string, 0, len(ollamaModelByRole))
			for name := range ollamaModelByRole {
				roleNamesWithOllama = append(roleNamesWithOllama, name)
			}
			sort.Strings(roleNamesWithOllama)
			for i := 0; i < len(roleNamesWithOllama); i++ {
				for j := i + 1; j < len(roleNamesWithOllama); j++ {
					a, b := roleNamesWithOllama[i], roleNamesWithOllama[j]
					if ollamaModelByRole[a] != ollamaModelByRole[b] {
						problems = append(problems, fmt.Sprintf("roles %s and %s both use ollama with different models; loading a second model may stall the host", a, b))
					}
				}
			}
		}
	}

	hub := mcpgate.NewHub()
	mcpConfigs := resolveForReport(args)
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
	residentNow := residentToolNames(hasSessionSearch, webSearchAllowed(settings.Permissions))
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
	logsLine := diag.Dir()
	if latest := diag.Latest(); latest != "" {
		logsLine += "  (latest: " + filepath.Base(latest) + ")"
	}
	lines = append(lines, fmt.Sprintf("logs       %s", logsLine))

	if permissionMode == "bypassPermissions" {
		problems = append(problems, "permission mode is bypassPermissions: every tool call runs unchecked")
	}
	for _, st := range statuses {
		if !st.OK {
			problems = append(problems, fmt.Sprintf("mcp %q is down: %s", st.Name, st.Error))
		}
	}
	lines = append(lines, "")
	if len(problems) == 0 {
		lines = append(lines, "No problems found.")
	} else {
		lines = append(lines, plural.Count(len(problems), "problem")+":")
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
	configs := resolveForReport(args)
	hub := mcpgate.NewHub()
	hub.ConnectAll(ctx, configs)
	defer hub.Close(ctx)
	fmt.Fprintln(stdout, mcpgate.RenderMCPReport(hub.Statuses()))
	return 0
}

// version implements `harness --version` / `-v`.
func VersionCmd(stdout io.Writer) int {
	fmt.Fprintln(stdout, version())
	return 0
}

// Version is the harness's own release version, set at build time via
// `-ldflags "-X github.com/andrepato/harness/internal/cli.Version=<ver>"`;
// "dev" otherwise. The startup banner's `harness v<version>` row
// (internal/cli/tui.go) reads this directly.
var Version = "dev"

// version reads the module's own version for `--version`.
func version() string {
	return "kiln " + Version + " (go port)"
}

// semverPrefix matches a real semver tag ("1.2.3", "v1.2.3", "1.2.3-4-g…"
// from `git describe`'s own suffix), but not a bare git short hash: a hash
// is hex digits with no dots ("61bae41"), so it never matches
// \d+\.\d+\.\d+ even though, like a semver, it can start with a digit.
var semverPrefix = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+`)

// versionLabel renders Version for the startup banner: "v1.2.3" for a
// real semver tag, and the bare string for anything else — a `git
// describe` short hash ("61bae41"), a dirty one ("61bae41-dirty"), or
// "dev" — so a dev build never reads "v61bae41" or "vdev" (see
// qa/findings/20260926T231105Z-banner-version-hash.json: a hex hash
// starting with a digit was wrongly treated as numeric-therefore-semver
// by the previous `Version[0] >= '0' && Version[0] <= '9'` check).
func versionLabel(v string) string {
	if semverPrefix.MatchString(v) {
		return "v" + strings.TrimPrefix(v, "v")
	}
	return v
}

// resolveForReport is every MCP server a session here would start: all
// scopes (project ones only in a trusted folder) plus --mcp-config.
func resolveForReport(args Args) map[string]mcpgate.ServerConfig {
	cwd, _ := os.Getwd()
	r := mcpgate.Resolve(mcpgate.ResolveOptions{Cwd: cwd, Path: args.MCPConfig, Strict: args.StrictMCPConfig})
	if folderTrusted(cwd) {
		return r.All()
	}
	return r.Servers
}
