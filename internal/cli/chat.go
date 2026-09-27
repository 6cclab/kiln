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
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/auth/login"
	"github.com/andrepato/harness/internal/budget"
	claudeagents "github.com/andrepato/harness/internal/claude/agents"
	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	claudekeybindings "github.com/andrepato/harness/internal/claude/keybindings"
	claudememory "github.com/andrepato/harness/internal/claude/memory"
	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/skills"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/execenv"
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

// defaultSystemPrompt is cli.ts's fallback when --system-prompt is absent.
const defaultSystemPrompt = "You are a coding assistant operating in a terminal."

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
	// Local models can take a while to load into memory on first use; the
	// package default (15s) is tuned for discovery calls, not generation.
	return ollama.Options{URL: url, ServerDefaultContext: serverDefault, HTTPClient: &http.Client{Timeout: 3 * time.Minute}}
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

// formatSkillsIndex renders the loaded skills as pi-agent-core's
// formatSkillsForSystemPrompt does (dist/harness/system-prompt.js):
// name/description/location per skill, inside <available_skills>.
//
// Deviation: pi's Skill carries disableModelInvocation and filters on it;
// this port's claude/skills.Skill has no such field (it only tracks
// UserInvocable, which controls slash-command exposure, a different
// question — see skills.go's doc comment). Every loaded skill is indexed
// here; nothing is hidden from the model for lack of that field.
func formatSkillsIndex(list []skills.Skill) string {
	if len(list) == 0 {
		return ""
	}
	lines := []string{
		"The following skills provide specialized instructions for specific tasks.",
		"Read the full skill file when the task matches its description.",
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool slashcommands.",
		"",
		"<available_skills>",
	}
	for _, s := range list {
		lines = append(lines,
			"  <skill>",
			fmt.Sprintf("    <name>%s</name>", escapeXML(s.Name)),
			fmt.Sprintf("    <description>%s</description>", escapeXML(s.Description)),
			fmt.Sprintf("    <location>%s</location>", escapeXML(s.FilePath)),
			"  </skill>",
		)
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

// usageRowContextTokens is /context's and /usage's context-occupancy
// figure: a single request's input+output tokens — the same figure the
// TUI's pinned status meter computes from its own EventUsage.UsageRow
// (internal/tui/bridge.go: "ContextUsed comes from the LAST request's
// input+output, never the running total"). row is nil until the first
// EventUsage arrives.
//
// This deliberately does NOT read row.TotalTokens: for the anthropic
// provider (internal/provider/api/anthropic_messages.go),
// TotalTokens = Input+Output+CacheRead+CacheWrite for THAT ONE request,
// so even TotalTokens on a single row would already differ from the
// status meter's Input+Output. The defect this fixes was one level worse
// than that, though: chat.go used to read ev.UsageTotals (the session's
// running SUM across every turn so far, session.SessionStats.Usage,
// accumulated turn by turn via msg.Usage.Add in
// internal/harness/turn.go), not a single row at all — a figure that
// only grows and was observed at 959.6k/1000k (96%) in a session whose
// pinned status meter simultaneously and correctly read 2%.
func usageRowContextTokens(row *msg.Usage) (int, bool) {
	if row == nil {
		return 0, false
	}
	return row.Input + row.Output, true
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
	settings := claudesettings.LoadSettings(cwd, claudesettings.LoadOptions{
		Sources: settingsSources(args.SettingSources),
		Extra:   args.Settings,
	})

	// Precedence: --model > settings.json (only in "provider/model" form —
	// see cli.ts's comment: settings.model may hold a Claude Code alias like
	// "opus[1m]" that names nothing on this machine) > HARNESS_MODEL > the
	// harness's own default.
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
	providerID, modelID, ok := splitProviderModel(wanted)
	if !ok {
		fmt.Fprintf(stderr, "kiln: invalid model %q: expected provider/model\n", wanted)
		return 1
	}
	if err := offlineGuard(providerID); err != nil {
		fmt.Fprintln(stderr, "kiln:", err)
		return 1
	}

	reg := buildRegistry()
	if p, ok := reg.Provider(providerID); ok {
		if err := p.RefreshModels(ctx); err != nil {
			// A refresh failure is not fatal here: static-catalog providers
			// never need one, and a dynamic one (Ollama) that fails to
			// refresh still resolves against whatever this process already
			// knew, surfacing as "unknown model" below if that is empty.
			fmt.Fprintf(stderr, "kiln: refreshing %s: %v\n", providerID, err)
		}
	}
	resolved, err := reg.Resolve(providerID, modelID)
	if err != nil {
		var tooSmall *budget.ContextTooSmallError
		if errors.As(err, &tooSmall) {
			fmt.Fprintln(stderr, tooSmall.Error())
			return 1
		}
		fmt.Fprintln(stderr, "kiln:", err)
		return 1
	}

	// modelRoles warnings: printed once, up front, so a broken role is
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
				fmt.Fprintf(stderr, "kiln: role %s\n", problem)
			}
		}
	}

	// Memory is budgeted against the tier: on a 32k model the system prompt
	// is capped at ~2k tokens, and an unbounded CLAUDE.md would eat the
	// session.
	memory := claudememory.LoadMemory(cwd, resolved.Tier.SystemPromptTokens)
	for _, path := range memory.Dropped {
		fmt.Fprintf(stderr, "memory over budget, not loaded: %s\n", path)
	}

	// --add-dir may be repeated, matching Claude Code's flag.
	addDirs := args.AddDir

	env := execenv.New(cwd)

	skillList := skills.LoadSkills(cwd)
	skillsIndex := formatSkillsIndex(skillList)

	permissionMode := resolvePermissionMode(args, settings)
	perms := settings.Permissions
	// Flags are additive to the settings files rather than replacing them:
	// --allowed-tools is "also allow this for this run", not "forget my
	// configuration". Deny still wins over both (permission.Decide checks
	// deny first).
	perms.Allow = append(append([]string{}, perms.Allow...), args.AllowedTools...)
	perms.Deny = append(append([]string{}, perms.Deny...), args.DisallowedTools...)

	roots := append([]string{cwd}, addDirs...)
	gate := permission.NewGate(permission.GateOptions{
		Permissions: perms,
		Roots:       roots,
		Mode:        permissionMode,
	})

	// --- MCP ---------------------------------------------------------
	// Ported from cli.ts's MCP block (src/cli.ts:180-348): connect every
	// configured server, resolve the active posture, build tool_search and
	// todo_write, and (when internal/search compiles — it does, as of this
	// phase; see the phase report) session_search.
	todos := agent.NewTodoStore()

	hub := mcpgate.NewHub()
	mcpConfigs := mcpgate.ResolveConfigs(args.MCPConfig, args.StrictMCPConfig)
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
	residentNow := residentToolNames(sessionSearch != nil)

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
		extras := []*tool.Tool{tools.ToolSearchTool(mcpTools, scope, gateState, onAdmit)}
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
	// Approving a plan leaves plan mode into the chosen mode, on the gate
	// itself (src/cli.ts:215-217: onApprove -> permissionGate.setMode).
	// Without this the gate stayed read-only after approval while the tool
	// result claimed otherwise.
	planController.OnApprove = func(mode string) {
		gate.SetMode(claudesettings.PermissionMode(mode))
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
	approvePlan := func(ctx context.Context, plan string) (tools.PlanDecision, error) {
		return planApprover.get()(ctx, plan)
	}
	exitPlanModeTool := tools.ExitPlanModeTool(planController, approvePlan)

	shells := agent.NewBackgroundShells()
	bgShellTools := tools.BackgroundShellTools(shells, env)

	mcpExtras, mcpIndexText := buildMCPExtras(mcpTools)
	extraTools := make([]*tool.Tool, 0, len(mcpExtras)+7)
	extraTools = append(extraTools, mcpExtras...)
	extraTools = append(extraTools, todoWrite, taskTool, exitPlanModeTool)
	extraTools = append(extraTools, bgShellTools...)
	if sessionSearch != nil {
		extraTools = append(extraTools, tools.SessionSearchTool(sessionSearch))
	}

	// System prompt assembly order, matching cli.ts exactly: base persona,
	// --append-system-prompt, the plan-mode prompt (only in plan mode),
	// memory, the skills index, the MCP tool index.
	buildSystemPrompt := func(mcpIndexText string) string {
		systemPromptBase := args.SystemPrompt
		if systemPromptBase == "" {
			systemPromptBase = defaultSystemPrompt
		}
		promptParts := []string{systemPromptBase, args.AppendSystemPrompt}
		if permissionMode == claudesettings.ModePlan {
			promptParts = append(promptParts, agent.PlanModePrompt)
		}
		promptParts = append(promptParts, memory.Text, skillsIndex, mcpIndexText)
		return strings.Join(nonEmpty(promptParts), "\n\n")
	}
	systemPrompt := buildSystemPrompt(mcpIndexText)
	phase("session start", "model", providerID+"/"+modelID, "tools", len(extraTools))

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
		ThinkingLevel:   args.Effort,
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
	phase("session started", "session", started.SessionID, "transcript", started.TranscriptPath)
	diag.L().Info("run log", "session", started.SessionID, "path", logPath)
	logHarnessEvents(started.Harness)

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
	// dispatcher.Parent is read only once a subagent is actually dispatched
	// (during a turn, from taskTool's Execute), by which point started is
	// always set — see the dispatcher construction above.
	dispatcher.Parent = started

	sessionID := started.SessionID
	transcriptPath := started.TranscriptPath
	// Rebound to the TUI's transcript once it starts (cli.ts's
	// onHookNotices callback, src/cli.ts:681-683); stderr until then.
	hookNotice := &rebindable[func(string)]{fn: hookNoticeSink(stderr)}
	notice := func(message string) { hookNotice.get()(message) }

	// ContextUsed (for /usage and /context) tracks the LAST request's
	// input+output tokens — never UsageTotals, which is the session's
	// running sum across every turn (session.SessionStats.Usage, added
	// turn by turn via msg.Usage.Add in internal/harness/turn.go) and so
	// grows far past the context window's actual size in any multi-turn
	// session. That mismatch was defect 1: /context's header derived
	// contextUsed from UsageTotals.TotalTokens (a cumulative, multi-turn
	// sum), while the TUI's pinned status meter derives its percentage
	// from UsageRow.Input+UsageRow.Output — the most recent single
	// request's tokens, i.e. what is actually resident in the context
	// window right now (internal/tui/bridge.go's EventUsage case,
	// "ContextUsed comes from the LAST request's input+output, never the
	// running total"). The two disagreed because they read different
	// fields; this reads the same one the status meter does, so /context
	// and the pinned meter now always agree.
	var usageMu sync.Mutex
	var lastUsageRow *msg.Usage
	started.Harness.Events().On(harness.EventUsage, func(ev harness.Event) {
		usageMu.Lock()
		defer usageMu.Unlock()
		if ev.UsageRow != nil {
			lastUsageRow = ev.UsageRow
		}
	})
	contextUsed := func() (int, bool) {
		usageMu.Lock()
		defer usageMu.Unlock()
		return usageRowContextTokens(lastUsageRow)
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
	usageByModel := map[string]msg.Usage{}
	addUsage := func(providerID, modelID string, u msg.Usage) {
		if u.TotalTokens == 0 && u.Input == 0 && u.Output == 0 {
			return
		}
		key := providerID + "/" + modelID
		usageByModelMu.Lock()
		usageByModel[key] = usageByModel[key].Add(u)
		usageByModelMu.Unlock()
	}
	started.Harness.Events().On(harness.EventUsage, func(ev harness.Event) {
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
		settingsLoadedFrom = append(settingsLoadedFrom, string(s))
	}

	// Hooks from .claude/settings.json, accumulated across scopes.
	hookConfig := claudehooks.LoadHooks(cwd)

	// The four events the TS parsed but never fired. Stop runs when the
	// parent's run ends; a blocking Stop hook is reported to the user, not
	// fed back into the model (this port does not re-prompt on Stop).
	// PreCompact runs when the harness starts compacting; the harness does
	// not tell us whether /compact or the threshold triggered it, so the
	// trigger is always "auto". SubagentStop runs from the dispatcher when
	// a subagent's run ends. Notification is wired where the permission
	// prompt is shown (internal/cli/tui.go).
	stopHookActive := false
	started.Harness.Events().On(harness.EventRunEnd, func(ev harness.Event) {
		outcome := claudehooks.RunHooks(claudehooks.RunOptions{
			Config: hookConfig,
			Event:  claudehooks.Stop,
			Payload: claudehooks.Payload{
				SessionID:      sessionID,
				TranscriptPath: transcriptPath,
				Cwd:            cwd,
				StopHookActive: &stopHookActive,
			},
			OnNotice: notice,
		})
		if outcome.Blocked != nil {
			notice("Stop hook asked to continue: " + outcome.Blocked.Reason)
		}
	})
	started.Harness.Events().On(harness.EventCompactionStart, func(ev harness.Event) {
		claudehooks.RunHooks(claudehooks.RunOptions{
			Config: hookConfig,
			Event:  claudehooks.PreCompact,
			Payload: claudehooks.Payload{
				SessionID:      sessionID,
				TranscriptPath: transcriptPath,
				Cwd:            cwd,
				Trigger:        "auto",
			},
			OnNotice: notice,
		})
	})
	dispatcher.OnSubagentStop = func(agentName string, sub *agent.Started) {
		subSession, subTranscript := sessionID, transcriptPath
		if sub != nil {
			subSession, subTranscript = sub.SessionID, sub.TranscriptPath
			addUsage(sub.Model.Provider, sub.Model.ID, sub.Harness.Stats().Usage)
		}
		claudehooks.RunHooks(claudehooks.RunOptions{
			Config: hookConfig,
			Event:  claudehooks.SubagentStop,
			Payload: claudehooks.Payload{
				SessionID:      subSession,
				TranscriptPath: subTranscript,
				Cwd:            cwd,
				StopHookActive: &stopHookActive,
			},
			OnNotice: notice,
		})
	}

	registry := buildCommandRegistry(registryDeps{
		Cwd:                cwd,
		Started:            started,
		Registry:           reg,
		Gate:               gate,
		Hooks:              hookConfig,
		Agents:             agentsList,
		Skills:             skillList,
		MCP:                mcpSess,
		Todos:              todos,
		Shells:             shells,
		SettingsLoadedFrom: settingsLoadedFrom,
		ModelRoles:         settings.ModelRoles,
		ModelLabel:         providerID + "/" + modelID,
		SessionRepo:        sessionRepo,
		SessionsDir:        sessionRepo.Root,
		ContextUsed:        contextUsed,
		FileReadTokens:     getFileReadTokens,
		UsageByModel:       getUsageByModel,
		MCPConfigPath:      mcpgate.ConfigPath(args.MCPConfig),
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
			Config:         hookConfig,
			ToolName:       call.Name,
			Args:           call.Arguments,
			SessionID:      sessionID,
			TranscriptPath: transcriptPath,
			Cwd:            cwd,
			Check: func(toolName, primaryArg string, hasPrimaryArg bool, args map[string]any) (*claudehooks.Blocked, error) {
				blocked, out, err := gate.CheckWithOutcome(ctx, permission.Request{ToolName: toolName, PrimaryArg: primaryArg, Args: args})
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
		})
		if err != nil {
			return harness.BeforeToolResult{}, err
		}
		if guard.Blocked != nil {
			primary, _ := permission.PrimaryArgOf(call.Arguments)
			recordBlocked(call.Name, primary, guard.Blocked.Reason)
			return harness.BeforeToolResult{Block: &harness.ToolBlock{Reason: guard.Blocked.Reason}}, nil
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
			Config:      hookConfig,
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
	phase("hooks SessionStart start")
	sessionStart := claudehooks.RunHooks(claudehooks.RunOptions{
		Config: hookConfig,
		Event:  claudehooks.SessionStart,
		Payload: claudehooks.Payload{
			SessionID:      sessionID,
			TranscriptPath: transcriptPath,
			Cwd:            cwd,
		},
		OnNotice: notice,
	})
	phase("hooks SessionStart end")

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
			LogPath:         logPath,
			Debug:           args.Debug,
			StatusLine:      settings.StatusLine,
			SetPlanApprover: func(fn tools.PlanApprover) { planApprover.set(fn) },
			SetHookNotice:   func(fn func(string)) { hookNotice.set(fn) },
			ModelLabel:      providerID + "/" + modelID,
			Resolved:        resolved,
			Started:         started,
			Gate:            gate,
			PlanController:  planController,
			Registry:        registry,
			Env:             env,
			Dispatcher:      dispatcher,
			HookConfig:      hookConfig,
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
		claudehooks.RunHooks(claudehooks.RunOptions{
			Config: hookConfig,
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

	exitCode := runPrintMode(ctx, args, started, gate, resolved, hookConfig, sessionStart, cwd, stdout, stderr, stdin, getBlocked, registry)

	// Nothing outlives the session: a background shell started during this
	// run must not hold a port open after the process exits. Killed BEFORE
	// SessionEnd fires, matching cli.ts's own ordering (src/cli.ts:653-659) —
	// hub.Close is already deferred above, so it runs last of the three.
	shells.KillAll()

	claudehooks.RunHooks(claudehooks.RunOptions{
		Config: hookConfig,
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

	runResult, promptErr := started.Lane.Prompt(ctx, prompt, resolvedMentions.Images)
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
	on(harness.EventCompactionStart, none)
	on(harness.EventCompactionEnd, none)
	on(harness.EventFault, func(ev harness.Event) []any { return []any{"err", ev.Err} })
	on(harness.EventHandlerError, func(ev harness.Event) []any { return []any{"hook", ev.HookName, "err", ev.Err} })
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
