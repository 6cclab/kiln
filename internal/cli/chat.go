// Package cli, this file: the `harness` chat path (interactive minus the
// TUI, i.e. print mode) — the Go port of harness/src/cli.ts's chat().
//
// Scope for this phase: everything cli.ts's chat() does up to and including
// the `-p` / print branch. MCP, `/slash` commands, subagents (`task`) and
// the interactive TUI are out of scope; the seams where they would plug in
// are marked `// phase 5:` (MCP + slash commands + session search),
// `// phase 6:` (subagents) and `// phase 7:` (the interactive TUI). No
// fake/stub code stands in for them: the seam is a comment, not a
// half-implementation.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andrepato/harness/internal/plural"
	"github.com/mattn/go-isatty"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/auth/login"
	"github.com/andrepato/harness/internal/automode"
	"github.com/andrepato/harness/internal/budget"
	claudeagents "github.com/andrepato/harness/internal/claude/agents"
	claudecommands "github.com/andrepato/harness/internal/claude/commands"
	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	claudekeybindings "github.com/andrepato/harness/internal/claude/keybindings"
	claudememory "github.com/andrepato/harness/internal/claude/memory"
	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/permission"
	claudeplugins "github.com/andrepato/harness/internal/claude/plugins"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/skills"
	"github.com/andrepato/harness/internal/claude/writesettings"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/gitfiles"
	"github.com/andrepato/harness/internal/harness"
	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/builtin"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/provider/ollama"
	"github.com/andrepato/harness/internal/search"
	"github.com/andrepato/harness/internal/session/jsonl"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

// defaultModel is cli.ts's own default: qwen3.8 pinned resident on the GPU
// on this machine, so it needs no load wait.
const defaultModel = "ollama/qwen3.8:latest"

// defaultSystemPrompt is the base prompt when --system-prompt is absent.
var defaultSystemPrompt = agent.BasePrompt

// buildRegistry constructs the provider registry chat.go and subcommands.go
// both need: every provider in the vendored pi-ai catalog, Ollama
// (discovering against OLLAMA_HOST/OLLAMA_BASE_URL), and the faux test
// provider when HARNESS_FAUX_ADDR is set. Shared with cmd/harness-providers's
// former buildRegistry, now folded in here.
func buildRegistry() *provider.Registry {
	store := auth.NewFileCredentialStore("")
	reg := provider.NewRegistry(store)
	builtin.RegisterAll(reg, store)
	reg.Register(ollama.New(ollamaOptionsFromEnv()))
	if fp, ok := fauxprovider.New(); ok {
		reg.Register(fp)
	}
	return reg
}

// ollamaOptionsFromEnv reads OLLAMA_HOST (cli.ts's own source), falling
// back to OLLAMA_BASE_URL, plus OLLAMA_CONTEXT_LENGTH for models that pin no
// num_ctx of their own.
func ollamaOptionsFromEnv() ollama.Options {
	url := os.Getenv("OLLAMA_HOST")
	if url == "" {
		url = os.Getenv("OLLAMA_BASE_URL")
	}
	var serverDefault int
	if v := os.Getenv("OLLAMA_CONTEXT_LENGTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			serverDefault = n
		}
	}
	// No HTTPClient: ollama's default streaming client bounds connecting,
	// not the response. A 3-minute whole-request Timeout here cut off
	// compactions and turns on a slow local model mid-stream.
	return ollama.Options{URL: url, ServerDefaultContext: serverDefault}
}

// refreshRoleProviders refreshes the model list of every provider a role
// points at, other than the one already refreshed for the main model.
//
// Without this a role on a dynamic provider that is not the parent's
// (an Anthropic session with `fast: ollama/...`) has an empty model list
// at dispatch time, so ResolveModel silently falls back to the parent and
// the role never fires. Roles are fixed for the session, so once at
// startup is enough; a failure is reported and otherwise ignored exactly
// like the main refresh above.
func refreshRoleProviders(ctx context.Context, reg *provider.Registry, roles map[string]string, skip string, stderr io.Writer) {
	done := map[string]bool{skip: true}
	for _, value := range roles {
		providerID, _, ok := splitProviderModel(value)
		if !ok || done[providerID] {
			continue
		}
		done[providerID] = true
		if p, ok := reg.Provider(providerID); ok {
			if err := p.RefreshModels(ctx); err != nil && stderr != nil {
				fmt.Fprintf(stderr, "kiln: refreshing %s for roles: %v\n", providerID, err)
			}
		}
	}
}

// splitProviderModel splits "provider/model" into its two halves. A model
// id may itself contain "/" (e.g. Ollama tags rarely do, but nothing rules
// it out), so this splits on the first "/" only.
func splitProviderModel(wanted string) (providerID, modelID string, ok bool) {
	idx := strings.IndexByte(wanted, '/')
	if idx <= 0 || idx == len(wanted)-1 {
		return "", "", false
	}
	return wanted[:idx], wanted[idx+1:], true
}

// settingsSources converts --setting-sources's string list into
// paths.Scope, or nil (meaning "every scope") when unset.
func settingsSources(raw []string) []paths.Scope {
	if raw == nil {
		return nil
	}
	out := make([]paths.Scope, 0, len(raw))
	for _, s := range raw {
		out = append(out, paths.Scope(s))
	}
	return out
}

// userOnlySources narrows sources (as settingsSources returns it: nil
// means "every scope") to just the user scope, respecting a caller's own
// --setting-sources restriction: if the caller already excluded "user",
// the narrowed result excludes it too (an empty, non-nil slice, which
// settings.LoadSettings's `wants` reads as "nothing").
func userOnlySources(sources []paths.Scope) []paths.Scope {
	if sources == nil {
		return []paths.Scope{paths.ScopeUser}
	}
	for _, s := range sources {
		if s == paths.ScopeUser {
			return []paths.Scope{paths.ScopeUser}
		}
	}
	return []paths.Scope{}
}

// trustedAutoMemoryDirectory resolves the autoMemoryDirectory setting
// kiln is allowed to honour: merged (every scope) when cwd is a trusted
// folder, but only the user's own settings (plus an explicit --settings
// file, which the person typed on the command line themselves) when it
// isn't — a repository's own, possibly untrusted, checked-in or local
// settings.json must not be able to point kiln's auto-memory read-only
// permission root at an arbitrary directory (e.g. ~/.ssh) before the
// person has trusted the folder. Matches Claude Code's own rule for this
// setting (docs: code.claude.com/docs/en/memory, "Storage location" —
// honoured from project/local settings only under the same workspace-
// trust rule as hooks).
func trustedAutoMemoryDirectory(cwd string, args Args, merged claudesettings.Settings) (dir string, ignoredUntrusted bool) {
	if folderTrusted(cwd) {
		return merged.AutoMemoryDirectory, false
	}
	userOnly := claudesettings.LoadSettings(cwd, claudesettings.LoadOptions{
		Sources: userOnlySources(settingsSources(args.SettingSources)),
		Extra:   args.Settings,
	})
	return userOnly.AutoMemoryDirectory, userOnly.AutoMemoryDirectory != merged.AutoMemoryDirectory
}

// resolvePermissionMode applies cli.ts's precedence: --permission-mode >
// HARNESS_PERMISSION_MODE > settings.permissions.defaultMode > "manual".
func resolvePermissionMode(args Args, settings claudesettings.Settings) claudesettings.PermissionMode {
	if args.PermissionMode != "" {
		return claudesettings.PermissionMode(args.PermissionMode)
	}
	if v := os.Getenv("HARNESS_PERMISSION_MODE"); v != "" {
		return claudesettings.PermissionMode(v)
	}
	if settings.Permissions.DefaultMode != "" {
		return settings.Permissions.DefaultMode
	}
	return claudesettings.ModeManual
}

// escapeXML matches pi-agent-core's system-prompt.js escapeXml exactly
// (dist/harness/system-prompt.js): the same five entities, in the same
// order.
func escapeXML(v string) string {
	v = strings.ReplaceAll(v, "&", "&amp;")
	v = strings.ReplaceAll(v, "<", "&lt;")
	v = strings.ReplaceAll(v, ">", "&gt;")
	v = strings.ReplaceAll(v, "\"", "&quot;")
	v = strings.ReplaceAll(v, "'", "&apos;")
	return v
}

// skillIndexEntry renders one skill's <skill> block. An empty
// description omits the <description> line entirely: formatSkillsIndex's
// "names only" fallback, for when even a one-line description cannot fit
// the remaining budget.
func skillIndexEntry(s skills.Skill, description string) []string {
	lines := []string{
		"  <skill>",
		fmt.Sprintf("    <name>%s</name>", escapeXML(s.Name)),
	}
	if description != "" {
		lines = append(lines, fmt.Sprintf("    <description>%s</description>", escapeXML(description)))
	}
	lines = append(lines,
		fmt.Sprintf("    <location>%s</location>", escapeXML(s.FilePath)),
		"  </skill>",
	)
	return lines
}

// estimateTextTokens is the same chars/4 heuristic used throughout this
// port (internal/claude/memory's own estimate, internal/compaction's
// EstimateTokens): rough, budgeting-only, never reported as exact.
func estimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

func linesTokens(lines []string) int {
	return estimateTextTokens(strings.Join(lines, "\n"))
}

// minDescriptionTokens is the floor below which a truncated description
// is judged not worth keeping over the bare (name + location) entry: a
// handful of characters of ellipsis-truncated description reads as noise,
// not a usable discovery hint.
const minDescriptionTokens = 5

// skillsListingPointer is appended when some skills could not fit the
// listing even bare (name + location only): never dropped outright, only
// unlisted here - the model can still invoke one of them by its exact
// name through the skill tool, and this line says so, the same promise
// internal/claude/memory's rule index makes for CLAUDE.md rules that
// don't fit their own budget.
func skillsListingPointer(names []string) string {
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return fmt.Sprintf("  <!-- %s not listed here for space; invoke any of them by exact name with the skill tool: %s -->",
		plural.Count(len(names), "more skill"), strings.Join(names, ", "))
}

// formatSkillsIndex renders the loaded skills as a Go port of the
// equivalent catalog Claude Code injects into the conversation for its
// own Skill tool: name/description/location per skill, inside
// <available_skills>, budgeted against budgetTokens (the tier's
// SkillsListingTokens - internal/budget, 1% of the context window,
// matching the share observed there).
//
// Priority order when not everything fits is skills.OrderForIndex's:
// project/local skills first, then the user's own, then plugin skills
// last — plugins are routinely the largest source of catalog bulk (a
// handful of enabled plugins can contribute more skills than a project
// and its user combined) and the skills a person did not author
// themselves are the ones this index gives up first.
//
// Truncation happens in two stages, same order Claude Code's own listing
// budget uses: first every description is held at full length; if that
// doesn't fit, descriptions shrink (truncated with an ellipsis) until
// they do; if a description would shrink below minDescriptionTokens, every
// entry instead goes bare (name + location, no description) rather than
// print noise. Only if even every entry bare still doesn't fit does a
// skill get left off the listing — in priority order, least-priority
// first — and even then it is never silently lost: skillsListingPointer
// names it.
//
// Deviation: pi's Skill carries disableModelInvocation and filters on it;
// this port's claude/skills.Skill has no such field (it only tracks
// UserInvocable, which controls slash-command exposure, a different
// question — see skills.go's doc comment). Every loaded skill is indexed
// here; nothing is hidden from the model for lack of that field.
func formatSkillsIndex(list []skills.Skill, budgetTokens int) string {
	if len(list) == 0 {
		return ""
	}
	ordered := skills.OrderForIndex(list)

	header := []string{
		"The following skills provide specialized instructions for specific tasks.",
		"Read the full skill file when the task matches its description.",
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool slashcommands.",
		"",
		"<available_skills>",
	}

	full := make([][]string, len(ordered))
	bare := make([][]string, len(ordered))
	fullTok := make([]int, len(ordered))
	bareTok := make([]int, len(ordered))
	fullTotal, bareTotal := 0, 0
	for i, s := range ordered {
		full[i] = skillIndexEntry(s, s.Description)
		bare[i] = skillIndexEntry(s, "")
		fullTok[i] = linesTokens(full[i])
		bareTok[i] = linesTokens(bare[i])
		fullTotal += fullTok[i]
		bareTotal += bareTok[i]
	}

	var body [][]string
	var overflow []string

	switch {
	case budgetTokens <= 0 || fullTotal <= budgetTokens:
		// Everything fits with full descriptions: no change.
		body = full

	case bareTotal > budgetTokens:
		// Even the bare form doesn't fit for everyone: keep as many as the
		// budget allows, in priority order; the rest are named in the
		// trailing pointer line instead of silently disappearing.
		used := 0
		for i := range ordered {
			if used+bareTok[i] > budgetTokens {
				overflow = append(overflow, ordered[i].Name)
				continue
			}
			used += bareTok[i]
			body = append(body, bare[i])
		}

	default:
		// Every skill fits bare; the room left over is split evenly across
		// descriptions, each truncated to fit. Below minDescriptionTokens
		// per entry, truncation stops helping - fall back to bare instead.
		remaining := budgetTokens - bareTotal
		perEntry := remaining / len(ordered)
		if perEntry < minDescriptionTokens {
			body = bare
		} else {
			maxChars := perEntry * 4
			for _, s := range ordered {
				desc := s.Description
				if len(desc) > maxChars {
					cut := maxChars - 1
					if cut < 0 {
						cut = 0
					}
					desc = strings.TrimRight(desc[:cut], " \t") + "…"
				}
				body = append(body, skillIndexEntry(s, desc))
			}
		}
	}

	lines := append([]string{}, header...)
	for _, e := range body {
		lines = append(lines, e...)
	}
	if p := skillsListingPointer(overflow); p != "" {
		lines = append(lines, p)
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n")
}

// nonEmpty drops empty strings, matching cli.ts's `.filter(Boolean)`.
func nonEmpty(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// hookNoticeSink builds an OnNotice func that writes hook activity to
// stderr, for the USER only — the model must never learn a hook rewrote or
// blocked its call, or it starts second-guessing the tool it just called
// (see cli.ts's own comment to that effect above its hook wiring).
func hookNoticeSink(stderr io.Writer) func(string) {
	return func(message string) {
		fmt.Fprintf(stderr, "hook: %s\n", message)
	}
}

// subagentEventSink builds an agent.Dispatcher.OnEvent that prints the same
// three lines the TUI's transcript does (src/tui/app.ts:644-654) — start,
// done, error — to stderr, plain (no color/bold: this port has no TUI yet,
// and stderr here is for the user, same audience the TUI lines are for).
// Tool-start events are not printed: cli.ts's own comment on this is that a
// subagent's tool calls are deliberately not echoed, only the dispatch, the
// model it landed on, and the result size. Kept as a plain func value, not
// inlined at the call site, so a future TUI can swap it for its own
// rendering without touching the wiring around it.
func subagentEventSink(stderr io.Writer) func(agent.SubagentEvent) {
	return func(e agent.SubagentEvent) {
		switch e.Kind {
		case agent.SubagentEventStart:
			note := ""
			if e.Inherited {
				note = " (inherited; the requested model is not on this provider)"
			}
			model := e.ModelID
			if e.ProviderID != "" {
				model = e.ProviderID + "/" + e.ModelID
			}
			kind := ""
			if e.ModelKind != "" {
				kind = " [" + e.ModelKind + "]"
			}
			fmt.Fprintf(stderr, "└ %s %s on %s%s%s\n", e.Agent, e.Description, model, kind, note)
		case agent.SubagentEventDone:
			fmt.Fprintf(stderr, "  %s finished - %d tool calls, %d chars returned, %d tokens\n", e.Agent, e.ToolCalls, e.Chars, e.Usage.TotalTokens)
		case agent.SubagentEventError:
			fmt.Fprintf(stderr, "%s: %s\n", e.Agent, e.Message)
		}
	}
}

// fileReadTokensFromToolEnd reports the tokens attributable to one
// EventToolEnd, for /context's "Files read" segment
// (docs/kiln-design-handoff/Terminal.dc.html line 227): only the "read"
// tool's (internal/tools/read.go) successful results count as file
// content read into the conversation. ok is false for every other tool,
// a nil result, or an error result — none of those actually added file
// content to the transcript.
func fileReadTokensFromToolEnd(toolName string, result *msg.ToolResultMessage) (int, bool) {
	if toolName != "read" || result == nil || result.IsError {
		return 0, false
	}
	return compaction.EstimateTokens(*result), true
}

// Run implements cli.ts's chat(): registry, settings, model resolution,
// memory, skills, the permission gate, the system prompt, the started
// session, hooks, and (this phase) the print-mode path. Interactive mode
// (no -p) is not yet implemented; see the final check below.
func Run(ctx context.Context, args Args, stdout, stderr io.Writer, stdin io.Reader) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "kiln:", err)
		return 1
	}

	// The run log (internal/diag): always on at Info, Debug with --debug.
	// Its path is reported by `harness doctor`; with --debug it is also
	// announced so a report can point at it.
	logPath, closeLog, logErr := diag.Start("", args.Debug)
	if logErr != nil {
		fmt.Fprintln(stderr, "kiln:", logErr)
	} else if args.Debug && args.Print {
		fmt.Fprintln(stderr, "kiln: debug log:", logPath)
	}
	defer closeLog()
	phase := func(name string, kv ...any) {
		diag.L().Info("phase "+name, append([]any{"elapsed", diag.Since()}, kv...)...)
	}
	phase("start", "cwd", cwd, "print", args.Print)

	// Settings are read before the model is chosen, because `model` may
	// come from them.
	// In a folder not yet trusted, the allow rules a repository can supply
	// wait for trust (claudesettings.LoadOptions.Trusted); so do hooks in
	// an interactive session (trustedHooks, below).
	trustedAtStart := folderTrusted(cwd)
	settings := claudesettings.LoadSettings(cwd, claudesettings.LoadOptions{
		Sources:  settingsSources(args.SettingSources),
		Extra:    args.Settings,
		Trusted:  trustedAtStart,
		Headless: args.Print,
	})

	// startupWarn reports a problem found while starting: to stderr in
	// print mode, and as a note under the banner interactively, where
	// stderr is hidden behind the fullscreen TUI until exit.
	var startupNotes []string
	startupWarn := func(msg string) {
		if args.Print {
			fmt.Fprintln(stderr, "kiln: "+msg)
			return
		}
		startupNotes = append(startupNotes, msg)
	}

	// Precedence: --model > the resumed session's own model > settings.json
	// (only in "provider/model" form — see cli.ts's comment: settings.model
	// may hold a Claude Code alias like "opus[1m]" that names nothing on
	// this machine) > HARNESS_MODEL > the harness's own default.
	//
	// A resumed session continues on the model it last ran on, as pi does
	// (agent.ResumedModel): its conversation was sized for that model's
	// window. Claude Code resumes on the settings model instead.
	configured := ""
	if strings.Contains(settings.Model, "/") {
		configured = settings.Model
	}
	if configured == "" {
		configured = os.Getenv("HARNESS_MODEL")
	}
	if configured == "" {
		configured = defaultModel
	}
	wanted := args.Model
	restoring := ""
	if wanted == "" {
		if ref, ok := agent.ResumedModel(agent.Options{Cwd: cwd, Resume: args.Resume, ResumeLatest: args.ResumeLatest || args.ContinueLatest}); ok {
			restoring = ref.Provider + "/" + ref.ModelID
			wanted = restoring
		}
	}
	if wanted == "" {
		wanted = configured
	}

	reg := buildRegistry()
	var providerID, modelID string
	var resolved provider.Resolved
	var resolveErr error
	for {
		var ok bool
		providerID, modelID, ok = splitProviderModel(wanted)
		if !ok {
			resolveErr = fmt.Errorf("invalid model %q: expected provider/model", wanted)
		} else if resolveErr = offlineGuard(providerID); resolveErr == nil {
			if p, ok := reg.Provider(providerID); ok {
				if err := p.RefreshModels(ctx); err != nil {
					// A refresh failure is not fatal here: static-catalog providers
					// never need one, and a dynamic one (Ollama) that fails to
					// refresh still resolves against whatever this process already
					// knew, surfacing as "unknown model" below if that is empty.
					fmt.Fprintf(stderr, "kiln: refreshing %s: %v\n", providerID, err)
				}
			}
			resolved, resolveErr = reg.Resolve(providerID, modelID)
		}
		if resolveErr == nil || restoring == "" || wanted != restoring {
			break
		}
		// The session's model is gone (or refused): say so and carry on
		// with the configured one, as pi does.
		startupWarn(fmt.Sprintf("Could not resume on this session's model %s (%v); using %s.", restoring, resolveErr, configured))
		wanted = configured
	}
	if resolveErr != nil {
		var tooSmall *budget.ContextTooSmallError
		if errors.As(resolveErr, &tooSmall) {
			fmt.Fprintln(stderr, tooSmall.Error())
			return 1
		}
		fmt.Fprintln(stderr, "kiln:", resolveErr)
		return 1
	}

	// modelRoles warnings: reported once, up front, so a broken role is
	// visible before it silently falls back to the parent model mid-run
	// (agents.ResolveModel never errors, it only falls back - see its doc
	// comment). Not fatal: a bad role is a misconfiguration to fix, not a
	// reason to refuse to start.
	if len(settings.ModelRoles) > 0 {
		refreshRoleProviders(ctx, reg, settings.ModelRoles, providerID, stderr)
		if available, err := reg.Available(ctx); err == nil {
			var candidates []claudeagents.Candidate
			for _, p := range available {
				for _, m := range p.Models() {
					candidates = append(candidates, claudeagents.Candidate{ID: m.ID, Provider: p.ID()})
				}
			}
			for _, problem := range claudeagents.ValidateRoles(settings.ModelRoles, candidates) {
				startupWarn("Model role " + problem + "; subagents asking for it run on the current model.")
			}
		}
	}

	// Memory is budgeted against the tier: on a 32k model the system prompt
	// is capped at ~2k tokens, and an unbounded CLAUDE.md would eat the
	// session.
	memory := claudememory.LoadMemory(cwd, resolved.Tier.SystemPromptTokens)
	if n := len(memory.Indexed); n > 0 {
		diag.L().Info("memory: rules indexed, not loaded in full", "count", n, "budget", resolved.Tier.SystemPromptTokens, "paths", memory.Indexed)
	}
	if n := len(memory.ExternalSkipped); n > 0 {
		startupWarn(fmt.Sprintf("%d CLAUDE.md import(s) of files outside this project not loaded: external imports need approval for the project (approve them in Claude Code). %s", n, strings.Join(memory.ExternalSkipped, ", ")))
	}
	if memory.OverBudget {
		startupWarn(fmt.Sprintf("CLAUDE.md files use ~%dk tokens, over this model's %dk memory budget; loaded anyway.", memory.EstimatedTokens/1000, resolved.Tier.SystemPromptTokens/1000))
	}

	// Claude Code's auto-memory index (MEMORY.md), read-only: whatever
	// budget CLAUDE.md/rules left of the tier's system-prompt ceiling.
	// kiln never writes here — see internal/claude/memory/automemory.go.
	autoMemoryDirectorySetting, autoMemoryDirIgnoredUntrusted := trustedAutoMemoryDirectory(cwd, args, settings)
	if autoMemoryDirIgnoredUntrusted {
		startupWarn("this project's autoMemoryDirectory setting is ignored until the folder is trusted; using the default auto-memory location.")
	}
	autoMemoryBudget := resolved.Tier.SystemPromptTokens - memory.EstimatedTokens
	autoMemory := claudememory.LoadAutoMemory(cwd, claudememory.AutoMemoryOptions{
		Directory:    autoMemoryDirectorySetting,
		Enabled:      settings.AutoMemoryEnabled,
		EnvDisabled:  os.Getenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY") == "1",
		SmallTier:    resolved.Tier.Name == "small",
		BudgetTokens: autoMemoryBudget,
	})
	if autoMemory.DirectoryOverrideRejected {
		startupWarn("autoMemoryDirectory points at an unsafe location (the filesystem root, the home directory, or an ancestor of it); using the default auto-memory location instead.")
	}
	diag.L().Info("auto-memory", "status", autoMemory.Status, "dir", autoMemory.Dir, "reason", autoMemory.Reason)

	// --add-dir may be repeated, matching Claude Code's flag.
	addDirs := args.AddDir

	env := execenv.New(cwd)

	skillList := skills.LoadSkills(cwd)

	// Active plugins (installed and enabled — internal/claude/plugins).
	// Loaded once here so skills, commands, agents, hooks and MCP servers
	// all see the same set for this run.
	// --setting-sources decides whose enabledPlugins count, as in Claude
	// Code. A plugin only the project enables still runs nothing before an
	// interactive session's folder is trusted: hooks and MCP servers wait
	// for the dialog.
	activePlugins := claudeplugins.LoadPluginsFrom(cwd, settingsSources(args.SettingSources))
	var pluginSkills []skills.Skill
	var pluginCommands []claudecommands.CommandFile
	var pluginAgents []claudeagents.Definition
	pluginHooks := claudehooks.Config{}
	pluginMCPServers := map[string]mcpgate.ServerConfig{}
	for _, p := range activePlugins {
		pluginSkills = append(pluginSkills, claudeplugins.Skills(p)...)
		pluginCommands = append(pluginCommands, claudeplugins.Commands(p)...)
		pluginAgents = append(pluginAgents, claudeplugins.Agents(p)...)
		for event, groups := range claudeplugins.Hooks(p) {
			pluginHooks[event] = append(pluginHooks[event], groups...)
		}
		for name, cfg := range claudeplugins.MCPServers(p) {
			pluginMCPServers[name] = cfg
		}
	}
	allSkills := append(append([]skills.Skill{}, skillList...), pluginSkills...)
	skillsIndex := formatSkillsIndex(allSkills, resolved.Tier.SkillsListingTokens)

	// The `skill` tool's catalog: every project/user/plugin skill except
	// ones marked disable-model-invocation:true (deliverable 3 —
	// UserInvocable:false skills ARE included here; that flag only hides
	// a skill from the slash palette, wired separately below).
	var skillRecords []tools.SkillRecord
	for _, s := range allSkills {
		if s.DisableModelInvocation {
			continue
		}
		skillRecords = append(skillRecords, tools.SkillRecord{
			Name:        s.Name,
			Description: s.Description,
			Body:        s.Content,
			Dir:         tools.SkillDir(s.FilePath),
		})
	}
	skillTool := tools.SkillTool(skillRecords)

	permissionMode := resolvePermissionMode(args, settings)
	perms := settings.Permissions
	// Flags are additive to the settings files rather than replacing them:
	// --allowed-tools is "also allow this for this run", not "forget my
	// configuration". Deny still wins over both (permission.Decide checks
	// deny first).
	perms.Allow = append(append([]string{}, perms.Allow...), args.AllowedTools...)
	perms.Deny = append(append([]string{}, perms.Deny...), args.DisallowedTools...)
	// Write(...)/MultiEdit(...)/NotebookEdit(...)/Glob(...) path rules:
	// Claude Code never consults them and warns at startup; kiln honours
	// the deny/ask ones as Edit/Read rules and ignores the allow ones
	// (settings/pathrules.go), and says so here. Like Claude Code, a
	// Glob(...) rule passed in --allowed-tools is not warned about. The
	// warnings go to stderr or the TUI's startup notes, never the model.
	for _, w := range claudesettings.FileRuleWarnings(perms, args.AllowedTools...) {
		startupWarn(w)
	}
	if w := heldRulesWarning(cwd, settings, args.Print); w != "" {
		startupWarn(w)
	}
	for _, f := range settings.IgnoredModes {
		startupWarn(fmt.Sprintf("Ignoring permissions.defaultMode in %s: auto and bypassPermissions apply only from user settings or --settings.", shortPath(cwd, f)))
	}

	// HARNESS_EXP_LEDGER (switch 1, experiments.go): the one path plan
	// mode's read-only enforcement exempts, resolved once here so the gate
	// and the plan-mode prompt (buildSystemPrompt, below) agree on it.
	var experimentLedgerPath string
	if ledgerEnabled() {
		experimentLedgerPath = ledgerPath(cwd)
	}

	roots := append([]string{cwd}, addDirs...)
	var readOnlyRoots []string
	if !autoMemory.Disabled {
		// Reads of Claude Code's auto-memory topic files (the read tool,
		// on demand) should not prompt, but a write/edit there is still
		// gated exactly as any other outside-workspace path: kiln never
		// writes auto memory.
		readOnlyRoots = append(readOnlyRoots, autoMemory.Dir)
	}
	gate := permission.NewGate(permission.GateOptions{
		Permissions:    perms,
		Roots:          roots,
		ReadOnlyRoots:  readOnlyRoots,
		Mode:           permissionMode,
		PlanLedgerPath: experimentLedgerPath,
	})
	// Every settings file this session reads, --settings included, is
	// reloaded when it changes (settings_reload.go), so a write to any of
	// them, or to where it really lives, needs the user's approval and is
	// held from sandboxed commands.
	settingsFiles := claudesettings.SettingsFiles(cwd, claudesettings.LoadOptions{Sources: settingsSources(args.SettingSources), Extra: args.Settings})
	gate.ProtectSettingsFiles(settingsFiles)
	// A "did you mean" hint (internal/execenv/didyoumean.go) scans a
	// failed read/edit/write's parent directory before it can suggest a
	// near-identical name; it must never reveal an entry from a directory
	// the gate would not otherwise let this session read silently. Gating
	// it on WithinRoots/WithinReadOnlyRoots keeps the hint inside exactly
	// the directories a read already reaches without a prompt - anything
	// that would need to ask (or would be refused) gets no hint instead.
	// The session scratchpad is read without a prompt in every mode too
	// (set later, once the session id is known; read at call time).
	env.DidYouMeanDirAllowed = func(dir string) bool {
		return gate.WithinRoots(dir) || gate.WithinReadOnlyRoots(dir) || gate.InScratchpad(dir)
	}

	// The OS sandbox for the bash tools (sandbox.go), bound to the gate
	// and to env before the tools are built.
	sandboxMgr, err := startSandbox(cwd, settings, perms, settingsFiles, gate, env, startupWarn)
	if err != nil {
		fmt.Fprintln(stderr, "kiln:", err)
		return 1
	}
	defer sandboxMgr.Close()

	// --- MCP ---------------------------------------------------------
	// Ported from cli.ts's MCP block (src/cli.ts:180-348): connect every
	// configured server, resolve the active posture, build tool_search and
	// todo_write, and (when internal/search compiles — it does, as of this
	// phase; see the phase report) session_search.
	todos := agent.NewTodoStore()

	hub := mcpgate.NewHub()
	// Every scope Claude Code reads (user, local, .mcp.json) plus
	// --mcp-config. A project's .mcp.json runs commands a clone can ship,
	// so its servers start only in a trusted folder: now, when it already
	// is, or (interactive) once the trust dialog is accepted.
	resolvedMCP := mcpgate.Resolve(mcpgate.ResolveOptions{Cwd: cwd, Path: args.MCPConfig, Strict: args.StrictMCPConfig})
	// Plugin servers belong alongside the ones the user added by hand
	// (mcpgate.ScopePlugin): the user already opted in by installing and
	// enabling the plugin, so — unlike .mcp.json — they are not gated on
	// folder trust.
	for name, cfg := range pluginMCPServers {
		resolvedMCP.Servers[name] = cfg
	}
	mcpConfigs := resolvedMCP.Servers
	pendingMCP := resolvedMCP.Project
	if folderTrusted(cwd) {
		mcpConfigs, pendingMCP = resolvedMCP.All(), nil
	}
	mcpCtx, cancelMCP := context.WithCancel(ctx)
	connectMCP := func(ctx context.Context) {
		phase("mcp connect start", "servers", len(mcpConfigs))
		hub.ConnectAll(ctx, mcpConfigs)
		phase("mcp connect end", "statuses", len(hub.Statuses()), "tools", len(hub.Tools()))
	}
	if args.Print {
		// Print mode has exactly one prompt, so the MCP catalog must be
		// complete before it runs. Interactive mode connects after the TUI
		// is up (internal/cli/tui.go) and registers the tools when done.
		connectMCP(mcpCtx)
		warnFailedServers(stderr, hub.Statuses())
		if len(pendingMCP) > 0 {
			fmt.Fprintf(stderr, "kiln: not starting %s from %s: this folder is not trusted (start kiln here interactively and trust it)\n",
				plural.Count(len(pendingMCP), "MCP server"), resolvedMCP.ProjectFile)
		}
	}
	defer hub.Close(context.Background())
	defer cancelMCP()

	activePosture := resolvePosture()
	gateState := mcpgate.NewGateState()
	mcpTools := hub.Tools()

	var sessionSearch *search.Search
	if s, err := search.Open(""); err == nil {
		sessionSearch = s
		defer sessionSearch.Close()
	} else {
		fmt.Fprintf(stderr, "kiln: session search unavailable: %v\n", err)
	}
	residentNow := residentToolNames(sessionSearch != nil, webSearchAllowed(perms))

	mcpSess := &mcpSession{
		tools:    mcpTools,
		posture:  activePosture,
		state:    gateState,
		resident: residentNow,
		tier:     resolved.Tier,
	}
	onAdmit := func(ctx context.Context, names []string) error {
		return mcpSess.regate()
	}
	// The MCP-derived pieces (tool_search over the catalog, one adapter per
	// server tool, the posture index in the prompt) are built from a tool
	// list so they can be rebuilt once the background connect finishes.
	buildMCPExtras := func(mcpTools []mcpgate.McpTool) ([]*tool.Tool, string) {
		scope := mcpSess.indexScope()
		extras := []*tool.Tool{tools.ToolSearchTool(mcpTools, scope, mcpSess.state, onAdmit)}
		for _, t := range mcpTools {
			extras = append(extras, mcpgate.ToHarnessTool(hub, t))
		}
		return extras, mcpgate.IndexPromptText(scopedMCPTools(mcpTools, scope))
	}
	todoWrite := tools.TodoTool(todos.Set)

	// Subagent roster: the built-in general-purpose agent first, then every
	// .claude/agents/*.md definition — matching cli.ts's
	// `[GENERAL_PURPOSE, ...(await loadAgents(cwd))]`. Loaded before the
	// tool list so `task`'s catalog and schema enum are correct on the
	// first turn.
	agentsList := append([]claudeagents.Definition{agent.GeneralPurpose}, claudeagents.LoadAgents(cwd)...)
	agentsList = append(agentsList, pluginAgents...)

	// sessionsDirEnv mirrors agent.Options.SessionsRoot's own default
	// resolution (internal/agent/session.go's unexported
	// defaultSessionsDirEnv): HARNESS_SESSIONS_DIR if set, else
	// jsonl.NewRepo's own "~/.harness/sessions" default. Resolved here
	// (rather than after Start, as an earlier phase had it) so the
	// dispatcher's subagent sessions and the main session agree on where
	// they live.
	sessionRepo, err := jsonl.NewRepo(os.Getenv("HARNESS_SESSIONS_DIR"))
	if err != nil {
		fmt.Fprintln(stderr, "kiln:", err)
		return 1
	}

	// task: dispatch is wired lazily, matching cli.ts's own
	// `dispatch: (req) => createDispatcher({...})(req)` — the Dispatcher's
	// Parent field is only set once agent.Start returns below, but the
	// closure is not invoked until the model actually calls the tool
	// during a turn, by which point it is always set (same reasoning as
	// mcpSess.lane, just below).
	dispatcher := &agent.Dispatcher{
		Registry:     reg,
		Gate:         gate,
		Agents:       agentsList,
		Roles:        settings.ModelRoles,
		SessionsRoot: sessionRepo.Root,
		Env:          env,
		OnEvent:      subagentEventSink(stderr),
	}
	dispatchFn := func(ctx context.Context, req tools.TaskRequest) (tools.TaskDispatchResult, error) {
		r, err := dispatcher.Dispatch(ctx, agent.DispatchRequest{
			Agent:       req.Agent,
			Description: req.Description,
			Prompt:      req.Prompt,
			Model:       req.Model,
			ToolCallID:  req.ToolCallID,
		})
		return tools.TaskDispatchResult{Text: r.Text, ToolCalls: r.ToolCalls, Chars: r.Chars, Model: r.Model, Usage: r.Usage}, err
	}
	taskTool := tools.TaskTool(dispatchFn, agentsList, settings.ModelRoles, resolved.Tier)

	// Plan mode: active iff the resolved permission mode is "plan" (either
	// --permission-mode plan or the settings/env-var equivalents
	// resolvePermissionMode already folded in above).
	planController := agent.NewPlanController()
	planController.SetActive(permissionMode == claudesettings.ModePlan)

	// baseEffort is what --effort/the settings file asked for; startEffort
	// is what the session actually starts at, which HARNESS_EXP_PLAN_EFFORT
	// (switch 4, internal/cli/experiments.go) can raise for the plan-mode
	// portion of the run only. laneRef is filled in once agent.Start returns
	// (below), so planController.OnApprove — built here, before the lane
	// exists — can revert the effort level when the plan it approves leaves
	// plan mode.
	baseEffort := effortOrSetting(args.Effort, settings.EffortLevel, resolved.Model.Api == provider.ApiAnthropicMessages)
	startEffort := baseEffort
	planEffort := planEffortOverride()
	if planEffort != "" && permissionMode == claudesettings.ModePlan {
		startEffort = planEffort
	}
	var laneRef *harness.Lane
	// HARNESS_EXP_AUDIT (switch 2, experiments.go): armed by an approval,
	// consumed by the OnAfterResponse hook below (registered once
	// started.Lane exists) the first time the model's turn ends with no
	// tool calls afterward.
	var auditArmed atomic.Bool
	// Approving a plan leaves plan mode into the chosen mode, on the gate
	// itself (src/cli.ts:215-217: onApprove -> permissionGate.setMode).
	// Without this the gate stayed read-only after approval while the tool
	// result claimed otherwise.
	planController.OnApprove = func(mode string) {
		gate.SetMode(claudesettings.PermissionMode(mode))
		if planEffort != "" && laneRef != nil {
			_ = laneRef.SetThinkingLevel(baseEffort)
		}
		if auditEnabled() {
			auditArmed.Store(true)
		}
	}
	// Bound to the TUI once it starts, exactly as cli.ts reassigns
	// approvePlan from runApp's onPlanApprover callback (src/cli.ts:675-677).
	// Until then, and for the whole of print mode, every plan is reported
	// back with cli.ts's own fallback: "No interactive approval available.
	// Describe the plan in your reply instead." (src/cli.ts:221-222).
	planApprover := &rebindable[tools.PlanApprover]{fn: func(ctx context.Context, plan string) (tools.PlanDecision, error) {
		return tools.PlanDecision{
			Kind:     tools.PlanDecisionRevise,
			Feedback: "No interactive approval available. Describe the plan in your reply instead.",
		}, nil
	}}
	if fn := headlessPlanApprover(); fn != nil {
		planApprover.set(fn)
	}
	if fn := headlessAskApprover(); fn != nil {
		tools.SetAskUserApprover(fn)
	}
	approvePlan := func(ctx context.Context, plan string) (tools.PlanDecision, error) {
		return planApprover.get()(ctx, plan)
	}
	exitPlanModeTool := tools.ExitPlanModeTool(planController, approvePlan)

	shells := agent.NewBackgroundShells()
	bgShellTools := tools.BackgroundShellTools(shells, env)

	mcpExtras, mcpIndexText := buildMCPExtras(mcpTools)
	extraTools := make([]*tool.Tool, 0, len(mcpExtras)+7)
	extraTools = append(extraTools, mcpExtras...)
	extraTools = append(extraTools, todoWrite, taskTool, exitPlanModeTool, skillTool, tools.WebSearchTool(), tools.AskUserQuestionTool())
	extraTools = append(extraTools, bgShellTools...)
	extraTools = append(extraTools, tools.WebFetchTool(nil))
	if sessionSearch != nil {
		extraTools = append(extraTools, tools.SessionSearchTool(sessionSearch))
	}

	// System prompt assembly order, matching cli.ts exactly: base persona,
	// --append-system-prompt, the plan-mode prompt (only in plan mode),
	// memory, the skills index, the MCP tool index.
	envBlock := environmentPrompt(ctx, cwd, time.Now())
	// scratchpadPrompt names the session scratchpad once it exists (it
	// needs the session id, known after agent.Start).
	var scratchpadPrompt string
	buildSystemPrompt := func(mcpIndexText string) string {
		systemPromptBase := args.SystemPrompt
		if systemPromptBase == "" {
			systemPromptBase = defaultSystemPrompt
		}
		promptParts := []string{systemPromptBase, envBlock, args.AppendSystemPrompt}
		if permissionMode == claudesettings.ModePlan {
			promptParts = append(promptParts, agent.PlanModePrompt)
			// HARNESS_EXP_LEDGER (switch 1, experiments.go): tell the model
			// about the one path plan mode now lets it write.
			if path := experimentLedgerPath; path != "" {
				promptParts = append(promptParts, ledgerPrompt(path))
			}
		}
		// HARNESS_EXP_REVIEW (switch 3, experiments.go): a reviewer subagent
		// before finishing, regardless of plan mode.
		if reviewEnabled() {
			promptParts = append(promptParts, reviewPrompt)
		}
		promptParts = append(promptParts, scratchpadPrompt, memory.Text, autoMemory.Text, skillsIndex, mcpIndexText)
		return strings.Join(nonEmpty(promptParts), "\n\n")
	}
	systemPrompt := buildSystemPrompt(mcpIndexText)
	phase("session start", "model", providerID+"/"+modelID, "tools", len(extraTools))
	// Captured here, at the same "session start" point the phase log
	// above marks, for /cost's one-line design summary's elapsed segment
	// (registryDeps.SessionStartedAt -> BuiltinDeps.SessionElapsed).
	sessionStartedAt := time.Now()

	started, err := agent.Start(ctx, agent.Options{
		Registry:        reg,
		Resolved:        resolved,
		Cwd:             cwd,
		SessionsRoot:    sessionRepo.Root,
		Resume:          args.Resume,
		ResumeLatest:    args.ResumeLatest || args.ContinueLatest,
		SessionID:       args.SessionID,
		ForkSession:     args.ForkSession,
		Name:            args.Name,
		ThinkingLevel:   startEffort,
		SystemPrompt:    systemPrompt,
		Env:             env,
		ExtraTools:      extraTools,
		ActiveToolNames: mcpSess.activeToolNames(),
	})
	if err != nil {
		fmt.Fprintln(stderr, "kiln:", err)
		return 1
	}
	mcpSess.lane = started.Lane
	laneRef = started.Lane
	phase("session started", "session", started.SessionID, "transcript", started.TranscriptPath)
	diag.L().Info("run log", "session", started.SessionID, "path", logPath)
	logHarnessEvents(started.Harness)

	autoModeConfig := claudesettings.LoadAutoMode(cwd, claudesettings.LoadOptions{Sources: settingsSources(args.SettingSources), Extra: args.Settings})
	if w := claudesettings.AutoModeIgnoredWarning(cwd, autoModeConfig.Ignored); w != "" {
		startupWarn(w)
	}
	// A project's modelRoles.fast is ordinary subagent configuration; that
	// the classifier does not use it matters only in auto mode, so it is a
	// startup warning there and a run-log line otherwise.
	if w := claudesettings.FastRoleIgnoredWarning(cwd, autoModeConfig.FastRoleIgnored); w != "" {
		if gate.Mode() == claudesettings.ModeAuto {
			startupWarn(w)
		} else {
			diag.L().Info("auto mode", "note", w)
		}
	}
	wireAutoMode(gate, reg, started, memory.Text, autoModeConfig)

	if dir := setupScratchpad(gate, cwd, started.SessionID); dir != "" {
		scratchpadPrompt = scratchpadInstructions(dir)
		started.Harness.SetSystemPrompt(buildSystemPrompt(mcpIndexText))
	}

	// applyMCP registers the catalog once the background connect is done:
	// adapters and a rebuilt tool_search into the tool set, the posture
	// index into the prompt, and a re-gate so the active list reflects the
	// tier's strategy over the real catalog.
	applyMCP := func() {
		mcpTools := hub.Tools()
		extras, idx := buildMCPExtras(mcpTools)
		mcpSess.setTools(mcpTools)
		started.Harness.AddTools(extras...)
		started.Harness.SetSystemPrompt(buildSystemPrompt(idx))
		if err := mcpSess.regate(); err != nil {
			diag.L().Warn("mcp regate failed", "err", err)
		}
		phase("mcp applied", "tools", len(mcpTools))
	}
	mcpSess.rebuild = applyMCP
	// dispatcher.Parent is read only once a subagent is actually dispatched
	// (during a turn, from taskTool's Execute), by which point started is
	// always set — see the dispatcher construction above.
	dispatcher.Parent = started
	dispatcher.UserHistory = func(ctx context.Context) []msg.Message { return automode.BranchMessages(ctx, started.Lane) }

	sessionID := started.SessionID
	transcriptPath := started.TranscriptPath
	// Rebound to the TUI's transcript once it starts (cli.ts's
	// onHookNotices callback, src/cli.ts:681-683); stderr until then.
	hookNotice := &rebindable[func(string)]{fn: hookNoticeSink(stderr)}
	notice := func(message string) { hookNotice.get()(message) }
	// hookActivity shows what a running Stop/SubagentStop hook chain is
	// doing on the TUI's busy row; "" clears it. Nothing outside the TUI.
	hookActivity := &rebindable[func(string)]{fn: func(string) {}}
	activity := func(text string) { hookActivity.get()(text) }
	// "Yes, and don't ask again" on a bash prompt saves its rules, as
	// Claude Code does, but to kiln's own .kiln/settings.local.json (the
	// file /permissions writes): kiln reads .claude, never writes it.
	gate.SetRuleSaver(func(rule string) {
		if err := writesettings.AddRule(cwd, writesettings.Allow, rule); err != nil {
			notice(fmt.Sprintf("could not save %s to %s: %v (it applies to this session only)", rule, writesettings.LocalSettingsPath(cwd), err))
		}
	})

	// A settings file changed mid-session reloads the permission rules, as
	// in Claude Code (settings_reload.go). heldApplied: the trust dialog
	// was accepted, so a repository-supplied .kiln/settings.local.json's
	// allow rules apply on a reload too.
	var heldApplied atomic.Bool
	settingsWatchCtx, stopSettingsWatch := context.WithCancel(ctx)
	defer stopSettingsWatch()
	reloader := settingsReloader{
		mu:      &sync.Mutex{},
		cwd:     cwd,
		opts:    claudesettings.LoadOptions{Sources: settingsSources(args.SettingSources), Extra: args.Settings, Headless: args.Print},
		gate:    gate,
		trusted: func() bool { return heldApplied.Load() || folderTrusted(cwd) },
		notice:  notice,
	}
	go reloader.watch(settingsWatchCtx, claudesettings.DefaultWatchInterval)

	// ContextUsed (for /usage and /context) is the lane's one context
	// estimate (harness.Lane.ContextTokens): the last request's measured
	// size while the model that measured it is still the one in use, and
	// the estimate the turn loop checks requests with after a switch. The
	// footer's meter and /model's size warning read the same figure, so
	// the three agree.
	contextUsed := func() (int, bool) {
		n, err := started.Lane.ContextTokens()
		return n, err == nil && n > 0
	}

	// fileReadTokens accumulates the tokens attributable to file contents
	// read into the conversation this session, for /context's "Files
	// read" segment (defect 2: docs/kiln-design-handoff/Terminal.dc.html
	// line 227 specifies a fifth segment the breakdown never produced).
	// There is no existing per-tool token ledger anywhere in the session
	// storage (session.SessionStats only totals MessageCount and overall
	// Usage — internal/session/types.go), so this is sourced live from
	// the read tool's own results as they land: every EventToolEnd for
	// the "read" tool (internal/tools/read.go's Name) is estimated with
	// compaction.EstimateTokens, the same char/4 heuristic the compactor
	// uses for every other message in the transcript.
	var fileReadMu sync.Mutex
	var fileReadTokens int
	started.Harness.Events().On(harness.EventToolEnd, func(ev harness.Event) {
		n, ok := fileReadTokensFromToolEnd(ev.ToolName, ev.ToolResult)
		if !ok {
			return
		}
		fileReadMu.Lock()
		fileReadTokens += n
		fileReadMu.Unlock()
	})
	getFileReadTokens := func() (int, bool) {
		fileReadMu.Lock()
		defer fileReadMu.Unlock()
		return fileReadTokens, true
	}

	// usageByModel accumulates this session's usage per "provider/model",
	// for /cost's by-model breakdown (internal/commands/builtins.go). The
	// parent's own turns are added here from EventUsage's per-turn delta
	// (UsageRow), keyed by started.Model at the time of the event — read
	// live rather than captured once, so a /model switch mid-session
	// attributes turns to whichever model actually ran them. A dispatched
	// subagent's usage is added separately, from OnSubagentStop below,
	// since that is the one dispatcher hook this file sets that neither
	// print mode's subagentEventSink nor the TUI's bridge.SubagentSink
	// (internal/tui/bridge.go) ever overwrites.
	var usageByModelMu sync.Mutex
	// Seeded from the session's own usage rows: a resumed session's /cost
	// covers its earlier runs too, as the footer's cost already does.
	usageByModel := started.Harness.UsageByModel(started.Model.Provider + "/" + started.Model.ID)
	addUsageAs := func(key string, u msg.Usage) {
		if u.TotalTokens == 0 && u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.CacheWrite == 0 {
			return
		}
		usageByModelMu.Lock()
		usageByModel[key] = usageByModel[key].Add(u)
		usageByModelMu.Unlock()
	}
	addUsage := func(providerID, modelID string, u msg.Usage) {
		addUsageAs(providerID+"/"+modelID, u)
	}
	started.Harness.Events().On(harness.EventUsage, func(ev harness.Event) {
		if ev.SideUsage != nil {
			// A call beside the conversation (auto mode's classifier),
			// under its own label.
			addUsageAs(ev.UsageSource, *ev.SideUsage)
			return
		}
		if ev.UsageRow == nil {
			return
		}
		addUsage(started.Model.Provider, started.Model.ID, *ev.UsageRow)
	})
	getUsageByModel := func() map[string]msg.Usage {
		usageByModelMu.Lock()
		defer usageByModelMu.Unlock()
		out := make(map[string]msg.Usage, len(usageByModel))
		for k, v := range usageByModel {
			out[k] = v
		}
		return out
	}

	settingsLoadedFrom := make([]string, 0, len(settings.LoadedFrom))
	for _, s := range settings.LoadedFrom {
		settingsLoadedFrom = append(settingsLoadedFrom, s.Label())
	}

	// /doctor's sandbox section: same sandboxReport `kiln doctor` uses
	// (internal/cli/subcommands.go), computed once here from the same
	// cwd/settings startSandbox above resolved its own manager from — a
	// resolve independent of sandboxMgr's actual instance, since
	// sandboxReport also reports the "off" and "enabled, unavailable"
	// cases a nil sandboxMgr can't distinguish on its own.
	sandboxLine, sandboxProblems := sandboxReport(cwd, settings)

	// Hooks from .claude/settings.json, accumulated across scopes, plus
	// every active plugin's own hooks (each already carrying
	// CLAUDE_PLUGIN_ROOT — see claudeplugins.Hooks).
	hookConfig := claudehooks.LoadHooksFrom(cwd, settingsSources(args.SettingSources))
	for event, groups := range pluginHooks {
		hookConfig[event] = append(hookConfig[event], groups...)
	}
	// Interactively, no hook runs before the folder is trusted, as in
	// Claude Code: they run commands a repository can ship. A -p run never
	// shows the dialog and runs them, as Claude Code does there.
	hooks := newTrustedHooks(hookConfig, args.Print || trustedAtStart)

	// The four events the TS parsed but never fired. PreCompact runs when
	// the harness starts compacting; the harness does not tell us whether
	// /compact or the threshold triggered it, so the trigger is always
	// "auto". Notification is wired where the permission prompt is shown
	// (internal/cli/tui.go).
	//
	// Stop and SubagentStop work as in Claude Code. They run when a turn is
	// about to end its run (harness OnBeforeStop: the model replied with no
	// tool calls), never for one the user interrupted or that failed, and
	// on the run's context, so Esc kills a slow one and its verdict is
	// discarded. A hook that blocks (exit 2, or "decision": "block") keeps
	// the run going: its reason goes to the model as a user message and the
	// model is asked again, with stop_hook_active true on the hook calls
	// that follow, for the rest of that run. {"continue": false} ends it.
	started.Harness.Hooks().OnBeforeStop(func(ctx context.Context, info harness.StopInfo) (harness.StopVerdict, error) {
		active := info.StopHookActive
		outcome := claudehooks.RunHooks(claudehooks.RunOptions{
			Ctx:    ctx,
			Config: hooks.get(),
			Event:  claudehooks.Stop,
			Payload: claudehooks.Payload{
				SessionID:            sessionID,
				TranscriptPath:       transcriptPath,
				Cwd:                  cwd,
				StopHookActive:       &active,
				LastAssistantMessage: lastReplyText(info.Last),
			},
			OnNotice: notice,
			OnHookStart: func(i int, cmds []claudehooks.Command) {
				activity(stopHookActivity(claudehooks.Stop, cmds, i))
			},
		})
		activity("")
		return stopVerdict(claudehooks.Stop, outcome, notice), nil
	})
	started.Harness.Events().On(harness.EventCompactionStart, func(ev harness.Event) {
		claudehooks.RunHooks(claudehooks.RunOptions{
			Config: hooks.get(),
			Event:  claudehooks.PreCompact,
			Payload: claudehooks.Payload{
				SessionID:      sessionID,
				TranscriptPath: transcriptPath,
				Cwd:            cwd,
				// Claude Code's PreCompact trigger is "manual" for
				// /compact and "auto" otherwise; kiln's overflow
				// compaction is automatic too.
				Trigger: map[bool]string{true: "manual", false: "auto"}[ev.CompactionTrigger == harness.TriggerManual],
			},
			OnNotice: notice,
		})
	})
	dispatcher.OnSubagentStop = func(_ context.Context, _ string, sub *agent.Started, _ string) {
		if sub != nil {
			addUsage(sub.Model.Provider, sub.Model.ID, sub.Harness.Stats().Usage)
		}
	}
	dispatcher.BeforeSubagentStop = func(ctx context.Context, _ string, sub *agent.Started, info harness.StopInfo) harness.StopVerdict {
		subSession, subTranscript := sessionID, transcriptPath
		if sub != nil {
			subSession, subTranscript = sub.SessionID, sub.TranscriptPath
		}
		active := info.StopHookActive
		outcome := claudehooks.RunHooks(claudehooks.RunOptions{
			Ctx:    ctx,
			Config: hooks.get(),
			Event:  claudehooks.SubagentStop,
			Payload: claudehooks.Payload{
				SessionID:            subSession,
				TranscriptPath:       subTranscript,
				Cwd:                  cwd,
				StopHookActive:       &active,
				LastAssistantMessage: lastReplyText(info.Last),
			},
			OnNotice: notice,
			OnHookStart: func(i int, cmds []claudehooks.Command) {
				activity(stopHookActivity(claudehooks.SubagentStop, cmds, i))
			},
		})
		activity("")
		return stopVerdict(claudehooks.SubagentStop, outcome, notice)
	}

	registry := buildCommandRegistry(registryDeps{
		Cwd:                cwd,
		Started:            started,
		Interactive:        !args.Print,
		Registry:           reg,
		Gate:               gate,
		Hooks:              hookConfig,
		Agents:             agentsList,
		Skills:             allSkills,
		Plugins:            activePlugins,
		PluginCommands:     pluginCommands,
		MCP:                mcpSess,
		Todos:              todos,
		Shells:             shells,
		SettingsLoadedFrom: settingsLoadedFrom,
		SandboxLine:        sandboxLine,
		SandboxProblems:    sandboxProblems,
		ModelRoles:         settings.ModelRoles,
		ModelLabel:         providerID + "/" + modelID,
		SessionRepo:        sessionRepo,
		SessionsDir:        sessionRepo.Root,
		ContextUsed:        contextUsed,
		FileReadTokens:     getFileReadTokens,
		UsageByModel:       getUsageByModel,
		SessionStartedAt:   sessionStartedAt,
		MCPConfigPath:      mcpgate.ConfigPath(args.MCPConfig),
		AutoMemoryStatus:   autoMemory.StatusLine(),
	}, hub)

	// blockedLog accumulates every before_tool refusal this run, whether it
	// came from a PreToolUse hook or from the permission gate — unlike
	// gate.Blocked(), which only ever sees the ones that reached Check().
	// print mode's "blocked" field (print.go's PrintResult.Blocked) reports
	// this combined log, so a hook-refused call is visible there too, not
	// just as a tool_end isError.
	var blockedMu sync.Mutex
	var blockedLog []string
	recordBlocked := func(toolName, primaryArg, reason string) {
		blockedMu.Lock()
		defer blockedMu.Unlock()
		blockedLog = append(blockedLog, fmt.Sprintf("%s(%s): %s", toolName, primaryArg, reason))
	}
	getBlocked := func() []string {
		blockedMu.Lock()
		defer blockedMu.Unlock()
		return append([]string(nil), blockedLog...)
	}

	// before_tool: hooks run FIRST, then the gate, so the gate judges what
	// will actually execute (a hook may have rewritten it) rather than what
	// the model proposed. GuardToolCall already encodes this ordering.
	started.Harness.Hooks().OnBeforeTool(func(ctx context.Context, call msg.ToolCall) (harness.BeforeToolResult, error) {
		// outcome is set by the Check closure below, which GuardToolCall
		// always calls (unless a PreToolUse hook already blocked the
		// call) — it is how the kiln TUI's tool block learns whether this
		// call was "approved" (answered at a prompt) or "auto-approved"
		// (a rule/mode let it through without asking). See
		// permission.Outcome's doc comment.
		var outcome permission.Outcome
		guard, err := claudehooks.GuardToolCall(claudehooks.GuardOptions{
			Config:         hooks.get(),
			ToolName:       call.Name,
			Args:           call.Arguments,
			SessionID:      sessionID,
			TranscriptPath: transcriptPath,
			Cwd:            cwd,
			Ctx:            ctx,
			Check: func(toolName, primaryArg string, hasPrimaryArg bool, args map[string]any, decision claudehooks.Decision, reason string) (*claudehooks.Blocked, error) {
				blocked, out, err := gate.CheckWithOutcome(ctx, permission.Request{ToolName: toolName, PrimaryArg: primaryArg, Args: args,
					CallID: call.ID, History: autoModeHistory(ctx, started.Lane),
					HookDecision: string(decision), HookReason: reason})
				outcome = out
				if err != nil {
					return nil, err
				}
				if blocked != nil {
					return &claudehooks.Blocked{Reason: blocked.Reason}, nil
				}
				return nil, nil
			},
			PrimaryArgOf: permission.PrimaryArgOf,
			OnNotice:     notice,
			CheckArgs: func(args map[string]any) error {
				return started.Harness.CheckToolArgs(call.Name, args)
			},
		})
		if err != nil {
			return harness.BeforeToolResult{}, err
		}
		if guard.Blocked != nil {
			primary, _ := permission.PrimaryArgOf(call.Arguments)
			recordBlocked(call.Name, primary, guard.Blocked.Reason)
			if guard.ByHook {
				outcome = permission.OutcomeHookBlocked
			}
			return harness.BeforeToolResult{Block: &harness.ToolBlock{Reason: guard.Blocked.Reason}, PermissionOutcome: string(outcome)}, nil
		}
		if guard.Args != nil {
			raw, err := json.Marshal(guard.Args)
			if err != nil {
				return harness.BeforeToolResult{}, err
			}
			return harness.BeforeToolResult{RewrittenArgs: raw, PermissionOutcome: string(outcome)}, nil
		}
		return harness.BeforeToolResult{PermissionOutcome: string(outcome)}, nil
	})

	// postToolCtxQueue carries PostToolUse additionalContext into the
	// model's NEXT request via the transform_context hook.
	//
	// Read directly (internal/harness/turn.go's commitToolResult): the
	// tool's own toolResult entry is committed to the branch (Storage.Commit
	// at ~turn.go:627) BEFORE invokeAfterTool runs (~turn.go:633) — so by
	// the time a PostToolUse hook sees the result, it has already been
	// written; mutating *msg.ToolResultMessage in this handler would not
	// change what is on disk. additionalContext therefore cannot be
	// appended to the tool result itself. Instead it rides the
	// transform_context seam (drive()'s loop calls invokeTransformContext
	// right before every assistant request, turn.go:183), which fires
	// again immediately after this tool result — within the same
	// operation, before the model's next turn — so the context still
	// reaches the very next request, just as a synthetic context message
	// rather than as part of the tool_result content block.
	// HARNESS_EXP_AUDIT (switch 2, experiments.go): auditArmed is set once,
	// by planController.OnApprove above, on the plan's approval. The first
	// response afterward that ends the turn with no tool calls (not a
	// pause_turn) gets Steer'd one follow-up instead of finishing; Steer
	// queues onto the existing inbox-drain path (turn.go's drainInbox),
	// which already keeps a turn open when something was queued, so no
	// turn-loop change is needed here.
	started.Harness.Hooks().OnAfterResponse(func(ctx context.Context, m *msg.AssistantMessage) error {
		if !auditArmed.Load() {
			return nil
		}
		if len(msg.ToolCallsOf(m.Content)) > 0 || m.StopReason == msg.StopPause {
			return nil
		}
		auditArmed.Store(false)
		return started.Lane.Steer(auditFollowUp)
	})

	var postToolCtxMu sync.Mutex
	var postToolCtxQueue []string
	started.Harness.Hooks().OnTransformContext(func(ctx context.Context, transcript []msg.Message) ([]msg.Message, error) {
		postToolCtxMu.Lock()
		pending := postToolCtxQueue
		postToolCtxQueue = nil
		postToolCtxMu.Unlock()
		if len(pending) == 0 {
			return transcript, nil
		}
		text := fmt.Sprintf("<hook-context>\n%s\n</hook-context>", strings.Join(pending, "\n\n"))
		return append(transcript, msg.UserMessage{
			Role:      msg.RoleUser,
			Content:   msg.Blocks{msg.Text(text)},
			Timestamp: time.Now().UnixMilli(),
		}), nil
	})

	started.Harness.Hooks().OnAfterTool(func(ctx context.Context, call msg.ToolCall, result *msg.ToolResultMessage) error {
		var toolResponse any
		if result != nil {
			toolResponse = result.Content
		}
		outcome := claudehooks.RunHooks(claudehooks.RunOptions{
			Config:      hooks.get(),
			Event:       claudehooks.PostToolUse,
			ToolName:    call.Name,
			HasToolName: true,
			Payload: claudehooks.Payload{
				SessionID:      sessionID,
				TranscriptPath: transcriptPath,
				Cwd:            cwd,
				ToolName:       call.Name,
				ToolInput:      call.Arguments,
				ToolResponse:   toolResponse,
			},
			OnNotice: notice,
			Ctx:      ctx,
		})
		if len(outcome.Context) > 0 {
			postToolCtxMu.Lock()
			postToolCtxQueue = append(postToolCtxQueue, outcome.Context...)
			postToolCtxMu.Unlock()
		}
		if outcome.Blocked != nil {
			// PostToolUse cannot undo a tool that already ran (the result is
			// already committed — see the doc comment above). Claude Code's
			// own semantics for this event are the same: a block decision
			// here can only flag the run, not retroactively refuse the
			// call. Reported to the user, matching the Stop-hook block
			// handling above (~chat.go:693-695), not fed back to the model.
			notice("PostToolUse hook blocked (tool already ran, cannot be undone): " + outcome.Blocked.Reason)
		}
		return nil
	})

	// SessionStart fires once, before the first turn. Its stdout becomes
	// context for that first prompt only (see the <hook-context> wrapping
	// below).
	runSessionStart := func() claudehooks.Outcome {
		phase("hooks SessionStart start")
		defer phase("hooks SessionStart end")
		return claudehooks.RunHooks(claudehooks.RunOptions{
			Config: hooks.get(),
			Event:  claudehooks.SessionStart,
			Payload: claudehooks.Payload{
				SessionID:      sessionID,
				TranscriptPath: transcriptPath,
				Cwd:            cwd,
			},
			OnNotice: notice,
		})
	}
	// Before trust no hook is active, so this runs none; the session
	// starts for hooks once the dialog is accepted (TrustAccepted).
	sessionStart := runSessionStart()

	if !args.Print {
		// phase 7: the interactive TUI. Everything above (registry,
		// settings, model, memory, skills, the gate, the started session,
		// hooks) is already wired; RunInteractive (internal/cli/tui.go)
		// binds the gate's prompter, the dispatcher's subagent sink, and
		// drives the Bubbletea program.
		if stdinFile, ok := stdin.(*os.File); !ok || !isatty.IsTerminal(stdinFile.Fd()) {
			fmt.Fprintln(stderr, "kiln: interactive mode requires a TTY on stdin; use -p")
			_ = started.Harness.Close()
			return 2
		}
		// User keybindings, as Claude Code reads them, reported before the
		// TUI is built (src/cli.ts:664-669).
		keys := claudekeybindings.Load(claudekeybindings.Path())
		if keys.Error != "" {
			fmt.Fprintf(stderr, "keybindings: %s\n", keys.Error)
		}
		for _, conflict := range keys.Conflicts {
			fmt.Fprintf(stderr, "keybindings: %s\n", conflict)
		}
		phase("tui start")
		exitCode := RunInteractive(ctx, InteractiveDeps{
			StartupNotes:   startupNotes,
			Cwd:            cwd,
			Effort:         args.Effort,
			AuthKind:       authKindLabel(ctx, reg, providerID),
			Keybindings:    keys.Bindings,
			MCPServerCount: len(mcpConfigs),
			ConnectMCP: func(progress func(mcpgate.ServerStatus)) []mcpgate.ServerStatus {
				hub.OnServer = progress
				connectMCP(mcpCtx)
				applyMCP()
				return hub.Statuses()
			},
			PendingMCPCount: len(pendingMCP),
			// The allow rules held until the folder is trusted, re-read
			// from the files as they are now, and the hooks held with them.
			ApplyHeldRules: func() {
				heldApplied.Store(true)
				// On the TUI's goroutine: rules apply before the next
				// prompt, and a note (an unreadable file) is sent from
				// another goroutine so it cannot wait on this one.
				onTrust := reloader
				onTrust.notice = func(s string) { go notice(s) }
				onTrust.applyTrust()
				hooks.enable()
			},
			// SessionStart for a session whose hooks waited for trust.
			TrustedSessionStart: func() []string {
				if trustedAtStart {
					return nil
				}
				return runSessionStart().Context
			},
			ConnectPendingMCP: func(progress func(mcpgate.ServerStatus)) []mcpgate.ServerStatus {
				hub.OnServer = progress
				before := len(hub.Statuses())
				phase("mcp project connect start", "servers", len(pendingMCP))
				hub.ConnectAll(mcpCtx, pendingMCP)
				applyMCP()
				return hub.Statuses()[before:]
			},
			LogPath:         logPath,
			Debug:           args.Debug,
			StatusLine:      settings.StatusLine,
			SetPlanApprover: func(fn tools.PlanApprover) { planApprover.set(fn) },
			SetHookNotice:   func(fn func(string)) { hookNotice.set(fn) },
			SetHookActivity: func(fn func(string)) { hookActivity.set(fn) },
			ModelLabel:      providerID + "/" + modelID,
			Resolved:        resolved,
			Started:         started,
			Gate:            gate,
			PlanController:  planController,
			Registry:        registry,
			Env:             env,
			Dispatcher:      dispatcher,
			Hooks:           hooks.get,
			SessionStart:    sessionStart,
			ScreenReader:    args.ScreenReader,
			Fullscreen:      args.Fullscreen,
			// Both --resume/-r (args.ResumeSet) and --continue/-c
			// (args.ContinueLatest) resume an existing session — IsResume's
			// own doc comment on InteractiveDeps already says so — but this
			// used to read args.ResumeSet alone, so a --continue run never
			// replayed its prior transcript at startup (this flag is also
			// what NewModel's commitBanner gates that replay on) and still
			// showed the "Recent sessions" block it should have suppressed
			// (defect *resumed-session-no-transcript-replay).
			IsResume: args.ResumeSet || args.ContinueLatest,
		}, stdout, stderr, stdin)

		shells.KillAll()
		execenv.KillLeftoverJobs()
		claudehooks.RunHooks(claudehooks.RunOptions{
			Config: hooks.get(),
			Event:  claudehooks.SessionEnd,
			Payload: claudehooks.Payload{
				SessionID:      sessionID,
				TranscriptPath: transcriptPath,
				Cwd:            cwd,
				Reason:         "exit",
			},
		})
		_ = started.Harness.Close()
		return exitCode
	}

	exitCode := runPrintMode(ctx, args, started, gate, resolved, hooks.get(), sessionStart, cwd, stdout, stderr, stdin, getBlocked, registry)

	// Nothing outlives the session: a background shell started during this
	// run must not hold a port open after the process exits. Killed BEFORE
	// SessionEnd fires, matching cli.ts's own ordering (src/cli.ts:653-659) —
	// hub.Close is already deferred above, so it runs last of the three.
	shells.KillAll()
	execenv.KillLeftoverJobs()

	claudehooks.RunHooks(claudehooks.RunOptions{
		Config: hooks.get(),
		Event:  claudehooks.SessionEnd,
		Payload: claudehooks.Payload{
			SessionID:      sessionID,
			TranscriptPath: transcriptPath,
			Cwd:            cwd,
			Reason:         "exit",
		},
	})
	_ = started.Harness.Close()
	return exitCode
}

// readStdin reads r fully. Lets `git diff | harness -p "review this"` work.
func readStdin(r io.Reader) string {
	if r == nil {
		return ""
	}
	data, _ := io.ReadAll(r)
	return string(data)
}

// runPrintMode is cli.ts's `-p` branch: resolve the prompt, run
// UserPromptSubmit hooks and mentions, drive one turn through the lane, and
// render the result.
//
// Deviation from cli.ts: cli.ts's actual print branch (src/cli.ts:601-661)
// never calls runHooks for UserPromptSubmit at all — only the interactive
// TUI (src/tui/app.ts:920-935) wraps a prompt in <hook-context>. Since this
// port has no TUI yet and -p is the only path that runs a turn, the phase
// brief calls for that same UserPromptSubmit + SessionStart context
// wrapping here, in print mode, rather than leaving it absent until phase 7.
// That is the one place this implementation intentionally diverges from
// cli.ts's control flow instead of following it.
func runPrintMode(ctx context.Context, args Args, started *agent.Started, gate *permission.Gate, resolved provider.Resolved, hookConfig claudehooks.Config, sessionStart claudehooks.Outcome, cwd string, stdout, stderr io.Writer, stdin io.Reader, getBlocked func() []string, registry *slashcommands.Registry) int {
	if args.MaxTurnsErr != "" {
		fmt.Fprintln(stderr, args.MaxTurnsErr)
		return 1
	}

	promptText := args.PrintPrompt
	if promptText == "" {
		promptText = readStdin(stdin)
	}
	typed := promptText // what auto mode's classifier reads as the user's request
	if strings.TrimSpace(promptText) == "" {
		fmt.Fprintln(stderr, `usage: kiln -p "your prompt"   (or pipe text on stdin)`)
		return 1
	}

	// Slash commands run under -p too, matching cli.ts: `harness -p "/agents"`
	// should print the roster, not ask the model to describe it from the
	// tool catalog. A command whose Result carries a Prompt (e.g. /init,
	// a skill, a .claude/commands file) becomes the model's prompt instead
	// of the original text — mentions are then resolved against THAT text,
	// not re-resolved against the original, matching cli.ts's own
	// `resolveMentions(handledCommand?.prompt ?? promptText, ...)`. A
	// command with no Prompt (the common case: a report or a modal's text
	// fallback) prints its Output and returns without running a turn.
	if strings.HasPrefix(strings.TrimSpace(promptText), "/") {
		result, err := registry.Execute(ctx, strings.TrimSpace(promptText))
		if err == nil {
			// Print mode has no screen to keep live: a background
			// command (/compact) simply runs here, cancelled with ctx.
			result, err = slashcommands.RunBackground(ctx, result)
		}
		if err != nil {
			fmt.Fprintln(stderr, "kiln:", err)
			return 1
		}
		if result != nil {
			if result.Prompt == "" {
				for _, line := range result.Output {
					fmt.Fprintln(stdout, line)
				}
				if isUnknownCommandResult(*result) {
					return 1
				}
				return 0
			}
			promptText = result.Prompt
		}
	}

	notice := hookNoticeSink(stderr)

	promptHooks := claudehooks.RunHooks(claudehooks.RunOptions{
		Config: hookConfig,
		Event:  claudehooks.UserPromptSubmit,
		Payload: claudehooks.Payload{
			SessionID:      started.SessionID,
			TranscriptPath: started.TranscriptPath,
			Cwd:            cwd,
			Prompt:         promptText,
		},
		OnNotice: notice,
	})
	if promptHooks.Blocked != nil {
		fmt.Fprintf(stderr, "blocked by hook: %s\n", promptHooks.Blocked.Reason)
		return 1
	}

	hookContext := append([]string{}, sessionStart.Context...)
	hookContext = append(hookContext, promptHooks.Context...)

	// `@path` inlines files here too. A prompt that behaves differently
	// under -p than it does interactively is a trap.
	resolvedMentions, _ := ResolveMentions(promptText, Options{
		Cwd:   cwd,
		Tier:  &resolved.Tier,
		Roots: gate.Roots(),
	})
	for _, m := range resolvedMentions.Mentions {
		if m.Skipped != "" {
			fmt.Fprintf(stderr, "· @%s - %s\n", m.Raw, m.Skipped)
		}
	}

	prompt := resolvedMentions.Prompt
	if len(hookContext) > 0 {
		prompt = fmt.Sprintf("<hook-context>\n%s\n</hook-context>\n\n%s", strings.Join(hookContext, "\n\n"), prompt)
	}

	format := args.OutputFormat
	if format == "" {
		format = "text"
	}

	collector := NewCollector(started.Harness.Events())
	defer collector.Unsubscribe()
	collector.GetBlocked = getBlocked
	if format == "stream-json" {
		collector.Stream = func(ev StreamEvent) { WriteStreamEvent(stdout, ev) }
	}

	var unsubVerbose func()
	if args.Verbose {
		unsubVerbose = started.Harness.Events().On(harness.EventToolStart, func(ev harness.Event) {
			arg := toolArg(ev.ToolArgs)
			shown := ""
			if arg != nil {
				shown = *arg
			}
			fmt.Fprintf(stderr, "· %s(%s)\n", ev.ToolName, shown)
		})
	}
	if unsubVerbose != nil {
		defer unsubVerbose()
	}

	// Finish any operation a previous process left running (see
	// agent.ResumeIncomplete's doc comment) before this prompt's own turn
	// starts, and before --max-turns starts counting below — a resumed
	// operation's turns belong to the interrupted run, not to this
	// invocation's own turn budget. Done after the collector above has
	// already subscribed to the harness's events, so the resumed
	// operation's own tool calls and final text are captured into this
	// run's PrintResult exactly like the new prompt's own turn: a resumed
	// run's reply is real output, not something to discard just because it
	// came from an interrupted operation instead of this call's own
	// prompt.
	agent.ResumeIncomplete(ctx, started, notice)

	// --max-turns (print mode only; the interactive TUI has a human who can
	// just stop typing, so this cap has no equivalent there). turn.go's
	// drive loop (internal/harness/turn.go) emits EventTurnEnd once per
	// iteration whether or not that turn made tool calls, and emits the
	// NEXT EventTurnStart only when it is about to loop back for another
	// assistant request. EventTurnEnd alone can't tell "this was the last
	// turn" from "more turns are coming" — but a subsequent EventTurnStart
	// can: it only fires when the harness is about to request again. So
	// once turnsAtEnd reaches MaxTurns, arm limitReached and cancel ctx
	// from the *next* EventTurnStart, not from EventTurnEnd itself. A run
	// that finishes exactly at turn N (final assistant message, no tool
	// calls) returns from drive() without ever emitting that next
	// EventTurnStart, so it is never cut off.
	var maxTurnsHit bool
	if args.MaxTurns > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()

		turnsAtEnd := 0
		limitReached := false
		unsubEnd := started.Harness.Events().On(harness.EventTurnEnd, func(harness.Event) {
			turnsAtEnd++
			if turnsAtEnd >= args.MaxTurns {
				limitReached = true
			}
		})
		defer unsubEnd()
		unsubStart := started.Harness.Events().On(harness.EventTurnStart, func(harness.Event) {
			if limitReached && !maxTurnsHit {
				maxTurnsHit = true
				cancel()
			}
		})
		defer unsubStart()
	}

	runResult, promptErr := started.Lane.PromptAs(ctx, prompt, typed, resolvedMentions.Images)
	ok := runResult.Status == harness.StatusCompleted
	reason := ""
	if maxTurnsHit {
		ok = false
		reason = "max-turns-exceeded"
	}
	stats := started.Harness.Stats()
	result := collector.Finish(ok, stats, reason)
	if rendered := FormatPrintResult(result, format); rendered != "" {
		fmt.Fprintln(stdout, rendered)
	}
	if maxTurnsHit {
		fmt.Fprintf(stderr, "kiln: stopped after %d turns (--max-turns)\n", args.MaxTurns)
	} else if promptErr != nil {
		fmt.Fprintln(stderr, "kiln:", promptErr)
	}
	if result.OK {
		return 0
	}
	return 1
}

// rebindable is a function slot the TUI rebinds after the pieces that
// call it (the exit_plan_mode tool, the tool-guard hooks) were already
// constructed with a value that reads through it. It stands in for
// cli.ts's plain `let approvePlan = ...` that runApp's callbacks reassign.
type rebindable[T any] struct {
	mu sync.Mutex
	fn T
}

func (r *rebindable[T]) get() T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fn
}

func (r *rebindable[T]) set(fn T) {
	r.mu.Lock()
	r.fn = fn
	r.mu.Unlock()
}

// logHarnessEvents mirrors the harness's event stream into the run log so
// a stuck or failed turn can be read back: runs, turns, tool calls,
// retries, compaction and faults, each with the lane and the fields a
// reader would ask for first.
func logHarnessEvents(h *harness.Harness) {
	on := func(t harness.EventType, fn func(ev harness.Event) []any) {
		h.Events().On(t, func(ev harness.Event) {
			diag.L().Info(string(ev.Type), append([]any{"lane", ev.Lane}, fn(ev)...)...)
		})
	}
	none := func(ev harness.Event) []any { return nil }
	status := func(ev harness.Event) []any { return []any{"status", ev.Status} }
	on(harness.EventRunStart, none)
	on(harness.EventRunEnd, status)
	on(harness.EventTurnStart, none)
	on(harness.EventTurnEnd, status)
	on(harness.EventToolStart, func(ev harness.Event) []any { return []any{"tool", ev.ToolName, "id", ev.ToolCallID} })
	on(harness.EventToolEnd, func(ev harness.Event) []any {
		isErr := ev.ToolResult != nil && ev.ToolResult.IsError
		return []any{"tool", ev.ToolName, "id", ev.ToolCallID, "error", isErr}
	})
	on(harness.EventRetryScheduled, func(ev harness.Event) []any {
		return []any{"attempt", ev.Attempt, "delay_ms", ev.DelayMs, "err", ev.RetryError}
	})
	on(harness.EventCompactionStart, func(ev harness.Event) []any {
		return []any{"trigger", ev.CompactionTrigger, "model", ev.CompactionModel}
	})
	on(harness.EventCompactionEnd, func(ev harness.Event) []any { return []any{"err", ev.Err} })
	on(harness.EventCompactionRetry, func(ev harness.Event) []any {
		return []any{"trigger", ev.CompactionTrigger, "part", ev.CompactionPart, "attempt", ev.Attempt, "err", ev.Err}
	})
	// One line per summary request as it is sent, not per streamed token.
	h.Events().On(harness.EventCompactionProgress, func(ev harness.Event) {
		if ev.CompactionOutputTokens == 0 {
			diag.L().Info("compaction_part", "lane", ev.Lane, "part", ev.CompactionPart, "parts", ev.CompactionParts,
				"model", ev.CompactionModel, "prompt_tokens", ev.CompactionPromptTokens)
		}
	})
	on(harness.EventFault, func(ev harness.Event) []any { return []any{"err", ev.Err} })
	on(harness.EventHandlerError, func(ev harness.Event) []any { return []any{"hook", ev.HookName, "err", ev.Err} })
	logRequestTiming(h)
}

// logRequestTiming logs one "request_timing" line per model response: time
// to the first streamed event, total time, and how many stream events
// arrived. A slow turn is otherwise indistinguishable between a slow
// provider (long ttft) and a slow client (short ttft, long stream).
func logRequestTiming(h *harness.Harness) {
	type timing struct {
		start, first time.Time
		events       int
	}
	var mu sync.Mutex
	open := map[string]*timing{}
	h.Events().On(harness.EventMessageStart, func(ev harness.Event) {
		mu.Lock()
		open[ev.EntryID] = &timing{start: time.Now()}
		mu.Unlock()
	})
	h.Events().On(harness.EventMessageUpdate, func(ev harness.Event) {
		mu.Lock()
		if t := open[ev.EntryID]; t != nil {
			if t.events == 0 {
				t.first = time.Now()
			}
			t.events++
		}
		mu.Unlock()
	})
	h.Events().On(harness.EventMessageEnd, func(ev harness.Event) {
		mu.Lock()
		t := open[ev.EntryID]
		delete(open, ev.EntryID)
		mu.Unlock()
		if t == nil {
			return
		}
		ttft := int64(-1)
		if !t.first.IsZero() {
			ttft = t.first.Sub(t.start).Milliseconds()
		}
		diag.L().Info("request_timing", "lane", ev.Lane, "ttft_ms", ttft, "total_ms", time.Since(t.start).Milliseconds(), "events", t.events)
	})
}

// authKindLabel is the banner's auth description for the active provider,
// standing in for Claude Code's "Claude Max" / "API Usage Billing":
// "Claude subscription" for a stored OAuth plan credential, "API key" for a
// key, the provider's own name for local providers such as Ollama, and ""
// when nothing is configured.
func authKindLabel(ctx context.Context, reg *provider.Registry, providerID string) string {
	if providerID == "ollama" {
		return "Ollama"
	}
	store := auth.NewFileCredentialStore("")
	for _, st := range login.Status(ctx, reg, store) {
		if st.ProviderID != providerID {
			continue
		}
		if !st.Authed {
			return ""
		}
		switch st.Kind {
		case "subscription":
			if providerID == "anthropic" {
				return "Claude subscription"
			}
			return "subscription"
		case "oauth":
			return "OAuth"
		default:
			// Claude Code's own label for API-key billing
			// (startup-default-home.txt row 3).
			return "API Usage Billing"
		}
	}
	return ""
}

// environmentPrompt tells the model where it is: the working directory, the
// repository, the platform and the date. Without it the model guesses paths
// (a live Opus 4.8 session read /Users/<user>/dev/api/handlers.go for a
// project elsewhere, and hit the outside-workspace prompt). Built once per
// session, so it never invalidates the prompt cache mid-session.
func environmentPrompt(ctx context.Context, cwd string, now time.Time) string {
	// Read from .git's files: this is built at startup, before the folder
	// is trusted, and git would read the repository's config.
	repo := "no"
	if r, ok := gitfiles.Find(cwd); ok {
		repo = "yes"
		if branch, born, ok := r.Branch(); ok && !born {
			repo = "yes (no commits yet)"
		} else if ok && branch != "" {
			repo += " (branch " + branch + ")"
		}
	}
	return fmt.Sprintf("<env>\nWorking directory: %s\nIs a git repository: %s\nPlatform: %s/%s\nToday's date: %s\n</env>",
		cwd, repo, runtime.GOOS, runtime.GOARCH, now.Format("2006-01-02"))
}

// effortOrSetting is the thinking level for a session: --effort, else the
// effortLevel from Claude Code's settings (as Claude Code itself reads it)
// for a Claude model, else unset, which leaves the model's own default.
// The setting is Claude Code's, so it is not applied to other providers:
// on some local Qwen models any thinking at all makes them loop
// (provider/reasoning.go).
func effortOrSetting(flag, setting string, claudeModel bool) string {
	if flag != "" {
		return flag
	}
	if !claudeModel {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(setting))
}
