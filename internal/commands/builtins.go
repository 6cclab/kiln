package commands

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/writesettings"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// BuiltinDeps is everything builtinCommands binds against. The package
// does not import internal/cli or internal/agent directly: the integrator
// supplies the few operations that need those (SwitchModel, OnClear,
// OnExit) as callbacks, matching the brief's "Deps struct with function
// fields so the package does not import cli."
type BuiltinDeps struct {
	// Lane is the main conversation lane: /compact and /tools read/act on
	// it directly, mirroring builtins.ts's session.lane.
	Lane *harness.Lane
	// Registry is the provider registry, for /model's picker and /cost's
	// rate lookup.
	Registry *provider.Registry
	// ModelRoles is settings.json's modelRoles map (role name ->
	// "provider/model"), for `/model roles` and future callers that need
	// to resolve a subagent's model against role config rather than a
	// literal provider/model. Nil when none are configured.
	ModelRoles map[string]string

	// CurrentModel returns the active provider and model id.
	CurrentModel func() (providerID, modelID string)
	// CurrentTier returns the tier currently in effect.
	CurrentTier func() budget.Tier
	// ModelLabel is the display label /context's header shows ("<model> ·
	// Nk of Mk tokens", docs/kiln-design-handoff/README.md "context" row).
	// Empty falls back to "<providerID>/<modelID>" from CurrentModel.
	ModelLabel func() string
	// ContextUsed, if set, reports tokens currently resident — the same
	// signal AccountDeps.ContextUsed (account_commands.go) reports for
	// /usage, duplicated here rather than shared because the two Deps
	// structs are independently constructed by the integrator and neither
	// imports the other's package.
	ContextUsed func() (int, bool)
	// SystemPromptTokens, if set, reports the *actual* assembled system
	// prompt's token count for /context's "System prompt" segment
	// (buildContextBreakdown), in place of the tier's fixed
	// SystemPromptTokens ceiling. false (or a nil func) falls back to
	// that ceiling.
	SystemPromptTokens func() (int, bool)
	// ToolSchemaTokens, if set, reports the actual serialized tool-schema
	// cost for /context's "Tools" segment, in place of
	// budget.ToolStrategyCost[tier.ToolStrategy]. false (or a nil func)
	// falls back to that fixed cost.
	ToolSchemaTokens func() (int, bool)
	// FileReadTokens, if set, reports the tokens attributable to file
	// contents read into the conversation this session (the "read" tool's
	// results), for /context's "Files read" segment
	// (docs/kiln-design-handoff/Terminal.dc.html line 227). A nil func (or
	// one that returns false) leaves the segment at zero rather than
	// inventing a figure — there is no fixed-budget fallback for this one,
	// unlike System/Tools, since nothing about it is a tier constant.
	FileReadTokens func() (int, bool)
	// SwitchModel applies a model switch: the integrator implements it
	// with agent.SetModel (which moves the lane's model AND the harness's
	// compaction settings to the new tier) followed by whatever tool
	// re-gating the new tier's strategy requires. It returns the new
	// tier so /model can report it.
	SwitchModel func(ctx context.Context, providerID, modelID string) (budget.Tier, error)
	// OnModelChanged, if set, is told about every successful switch so the
	// UI can follow.
	OnModelChanged func(label string, tier budget.Tier)

	// Agents is the subagent roster, for /agents and /status.
	Agents []agents.Definition

	// UsageByModel returns this session's accumulated usage, keyed by
	// "provider/model" — the parent's own turns under its current model,
	// plus every dispatched subagent's usage under whichever model it
	// actually ran on. Nil (or a nil return) means "not tracked", and
	// /cost falls back to just its single-model summary.
	UsageByModel func() map[string]msg.Usage
	// SessionElapsed reports how long this session has been running, for
	// /cost's one-line design summary ("Session: $0.27 · 66k tokens in
	// context · 71s", Terminal.dc.html:320). Nil (or a nil return of 0)
	// omits the elapsed segment rather than printing a false "0s".
	SessionElapsed func() time.Duration

	// SessionsDir is shown by /status.
	SessionsDir string

	// OnClear starts the conversation over: the model sees no earlier
	// turns afterwards.
	OnClear func(ctx context.Context) error
	OnExit  func()
}

// formatTokens renders a token count the way tui/transcript.ts's
// formatTokens does: millions get "m", thousands get "k", both with one
// decimal; anything under 1,000 is printed plain. "1000.0k" reads as a
// mistake, and million-token windows are ordinary.
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return strconv.Itoa(n)
	}
}

// buildContextBreakdown turns a tier's fixed budgets plus the session's
// live usage total into /context's structured result (ContextBreakdown,
// registry.go), for the kiln TUI's RenderContext (internal/tui/context.go)
// to draw a stacked bar and legend from. -p/print mode ignores this and
// reads Output instead, so a nil deps.ContextUsed (no usage yet this
// session) is not an error here — it just reports Used: 0.
//
// The five segments and the header are built to be self-consistent by
// construction, not just individually plausible:
// system+tools+filesRead+conversation always equals the header's "used"
// figure, and all five segments always sum to exactly Window (percentages
// sum to ~100%, not something over 100). This replaced a version where the
// header showed the session's
// real ContextUsed total while the legend's "System prompt"/"Tools" rows
// showed the tier's fixed *budgets* (big, conservative ceilings, not what
// was actually spent) — a session that had barely used any tokens yet
// (say 5k) could still show "System prompt 12.8k · Tools 7.6k" plus a
// "Free" computed as window-minus-real-used, summing to well over the
// window and leaving "Conversation" clamped to a lying zero. Deriving
// Free as the remainder after the other three (rather than independently
// as window-minus-used) is what makes the totals line up.
//
// System and Tools prefer a measured figure (deps.SystemPromptTokens/
// ToolSchemaTokens) over the tier's fixed budget when the caller has one
// to give — plumbing an actual measured tool-schema cost end to end is
// not wired by any integrator yet (nothing in this codebase computes a
// live per-session tool-schema token count outside the eval suite's own
// after-the-fact estimate from a recorded request), so Tools falls back
// to budget.ToolStrategyCost in practice today; the hook exists so that
// can change without another pass through this function's math.
func buildContextBreakdown(deps BuiltinDeps, t budget.Tier) *ContextBreakdown {
	used := 0
	if deps.ContextUsed != nil {
		if u, ok := deps.ContextUsed(); ok {
			used = u
		}
	}
	label := ""
	if deps.ModelLabel != nil {
		label = deps.ModelLabel()
	}
	if label == "" && deps.CurrentModel != nil {
		providerID, modelID := deps.CurrentModel()
		if providerID != "" {
			label = providerID + "/" + modelID
		} else {
			label = modelID
		}
	}

	window := t.ContextWindow

	system := t.SystemPromptTokens
	if deps.SystemPromptTokens != nil {
		if v, ok := deps.SystemPromptTokens(); ok {
			system = v
		}
	}
	system = clampRange(system, 0, window)

	tools := budget.ToolStrategyCost[t.ToolStrategy]
	if deps.ToolSchemaTokens != nil {
		if v, ok := deps.ToolSchemaTokens(); ok {
			tools = v
		}
	}
	tools = clampRange(tools, 0, window-system)

	filesRead := 0
	if deps.FileReadTokens != nil {
		if v, ok := deps.FileReadTokens(); ok {
			filesRead = v
		}
	}
	filesRead = clampRange(filesRead, 0, window-system-tools)

	// The measured context (used) is ground truth: it is the same figure
	// the pinned status meter reports. System is measured, but Tools is a
	// static per-strategy budget estimate (budget.ToolStrategyCost) and
	// can overshoot what a given provider actually bills — in a real
	// ollama session the estimates summed to 16.3k against a measured
	// 10.7k, which drove Conversation to zero and made the header
	// contradict the meter. Never report more occupancy than was
	// measured: give back the overshoot, estimates first.
	if used > 0 {
		if over := system + tools + filesRead - used; over > 0 {
			give := min(over, tools)
			tools -= give
			over -= give
			if over > 0 {
				give = min(over, filesRead)
				filesRead -= give
				over -= give
			}
			if over > 0 {
				system = max(system-over, 0)
			}
		}
	}

	conversation := clampRange(used-system-tools-filesRead, 0, window-system-tools-filesRead)

	free := window - system - tools - filesRead - conversation
	if free < 0 {
		free = 0
	}

	return &ContextBreakdown{
		ModelLabel: label,
		// The header reports system+tools+filesRead+conversation, not the
		// raw ContextUsed figure: they can otherwise disagree whenever a
		// segment above got clamped (see the doc comment), and a header
		// that does not match its own legend is the bug this rewrite
		// fixes.
		Used:   system + tools + filesRead + conversation,
		Window: window,
		Segments: []ContextSegment{
			{Label: "System prompt", Tokens: system},
			{Label: "Tools", Tokens: tools},
			{Label: "Files read", Tokens: filesRead},
			{Label: "Conversation", Tokens: conversation},
			{Label: "Free", Tokens: free},
		},
	}
}

// clampRange clamps v to [lo, hi], treating a hi below lo as lo (an
// already-exhausted budget clamps everything after it to zero rather than
// going negative).
func clampRange(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// modelCache caches Registry.Available for a few seconds: /model's
// argument completions run on every keystroke, and Available can reach the
// network.
type modelCache struct {
	at     time.Time
	models []provider.Model
}

func (c *modelCache) get(ctx context.Context, reg *provider.Registry) ([]provider.Model, error) {
	if c.models != nil && time.Since(c.at) < 5*time.Second {
		return c.models, nil
	}
	providers, err := reg.Available(ctx)
	if err != nil {
		return nil, err
	}
	var models []provider.Model
	for _, p := range providers {
		models = append(models, p.Models()...)
	}
	c.at = time.Now()
	c.models = models
	return models, nil
}

func modelID(m provider.Model) string { return m.Provider + "/" + m.ID }

func modelDescription(m provider.Model) string {
	tier := budget.TierFor(m.ContextWindow)
	return fmt.Sprintf("%s · %s · %s usable", formatTokens(m.ContextWindow), tier.Name, formatTokens(budget.UsableTokens(tier)))
}

// modelDialogDescription is /model's dialog-row description, per the work
// item's brief: "<context window, e.g. 128k context> · <tier name> tier".
// Distinct from modelDescription (used by argument completions and the
// print-mode listing, which also report usable tokens) since the dialog
// row has less room and the brief specifies this exact, shorter format.
func modelDialogDescription(m provider.Model) string {
	tier := budget.TierFor(m.ContextWindow)
	return fmt.Sprintf("%s context · %s tier", formatTokens(m.ContextWindow), tier.Name)
}

// modelRolesTable renders `/model roles`' listing: one row per configured
// role, its provider/model value, tier and usable-token budget - or an
// "(unresolved: ...)" note when agents.ValidateRoles flags that role's
// value as unusable. `kiln models` (internal/cli) renders the same shape
// against the same data, so a change here should be mirrored there.
func modelRolesTable(roles map[string]string, models []provider.Model) []string {
	if len(roles) == 0 {
		return []string{"no modelRoles configured (settings.json)"}
	}

	byID := map[string]provider.Model{}
	candidates := make([]agents.Candidate, 0, len(models))
	for _, m := range models {
		byID[m.Provider+"/"+m.ID] = m
		candidates = append(candidates, agents.Candidate{ID: m.ID, Provider: m.Provider})
	}

	// ValidateRoles' "<role>: <detail>" strings are split back apart here
	// so the table can key its "unresolved" column by role name; see its
	// doc comment for that format contract.
	problems := map[string]string{}
	for _, p := range agents.ValidateRoles(roles, candidates) {
		if idx := strings.Index(p, ": "); idx != -1 {
			problems[p[:idx]] = p[idx+2:]
		}
	}

	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)

	lines := []string{fmt.Sprintf("  %-12s %-30s %8s %10s", "role", "provider/model", "tier", "usable")}
	for _, name := range names {
		value := roles[name]
		if reason, bad := problems[name]; bad {
			lines = append(lines, fmt.Sprintf("  %-12s %-30s (unresolved: %s)", name, value, reason))
			continue
		}
		tier := budget.TierFor(byID[value].ContextWindow)
		lines = append(lines, fmt.Sprintf("  %-12s %-30s %8s %10s", name, value, tier.Name, formatTokens(budget.UsableTokens(tier))))
	}
	return lines
}

// BuiltinCommands returns the source for the ten commands builtins.ts
// registers (help is filled in by BindHelp once the owning registry
// exists).
func BuiltinCommands(deps BuiltinDeps) Source {
	cache := &modelCache{}

	cmds := []Command{
		{
			Name:        "help",
			Description: "Shortcuts and commands",
			Run: func(ctx context.Context, args string) (Result, error) {
				return Result{}, nil // filled in by BindHelp
			},
		},
		{
			Name:        "clear",
			Description: "Start a fresh session",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.OnClear != nil {
					if err := deps.OnClear(ctx); err != nil {
						return Result{}, err
					}
				}
				return Result{Output: []string{"Conversation cleared."}, Clear: true}, nil
			},
		},
		{
			Name:        "compact",
			Description: "Summarize history to free context",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.Lane == nil {
					return Result{Output: []string{"No active lane to compact."}}, nil
				}
				if err := deps.Lane.Compact(ctx, nil); err != nil {
					return Result{}, err
				}
				return Result{Output: []string{"Context compacted."}}, nil
			},
		},
		{
			Name:        "context",
			Description: "Show context window usage",
			Run: func(ctx context.Context, args string) (Result, error) {
				t := deps.CurrentTier()
				out := []string{
					fmt.Sprintf("window        %d", t.ContextWindow),
					fmt.Sprintf("tier          %s", t.Name),
					fmt.Sprintf("tools         %s (%d tokens)", t.ToolStrategy, budget.ToolStrategyCost[t.ToolStrategy]),
					fmt.Sprintf("system prompt %d max", t.SystemPromptTokens),
					fmt.Sprintf("reserved      %d", t.Compaction.ReserveTokens),
					fmt.Sprintf("available     %d for conversation", budget.UsableTokens(t)),
				}
				return Result{Output: out, Context: buildContextBreakdown(deps, t)}, nil
			},
		},
		{
			Name:        "cost",
			Description: "Tokens and spend this session",
			// The design (Terminal.dc.html:320) renders /cost as a single
			// system note: "Session: $0.27 · 66k tokens in context ·
			// 71s" — spend, context tokens, elapsed session time, no
			// rate table. *qa/findings/20260927T014721Z-cost-not-one-
			// line-note.json*: the old implementation instead always
			// printed a rate line plus a "by model:" block, never that
			// summary.
			//
			// This keeps the summary as Output's first line
			// unconditionally (a single line renders as the design's
			// note, tui/app.go's `len(handled.Output) == 1` case), and
			// appends the per-model breakdown only when more than one
			// model actually ran this session: with just one, the
			// breakdown would only repeat the summary's own total under
			// a "by model:" heading, so the single-model case matches
			// the design exactly, and the multi-model case (this
			// codebase's own model-routing feature) gets the extra
			// detail as a labelled block with the summary as its first
			// row, which reads better than hiding that split entirely.
			Run: func(ctx context.Context, args string) (Result, error) {
				byModel := map[string]msg.Usage{}
				if deps.UsageByModel != nil {
					byModel = deps.UsageByModel()
				}
				var totalCost float64
				for _, u := range byModel {
					totalCost += u.Cost.Total
				}

				contextTokens := 0
				if deps.ContextUsed != nil {
					if v, ok := deps.ContextUsed(); ok {
						contextTokens = v
					}
				}

				summary := fmt.Sprintf("Session: $%.2f · %s tokens in context", totalCost, formatTokens(contextTokens))
				if deps.SessionElapsed != nil {
					if d := deps.SessionElapsed(); d > 0 {
						summary += fmt.Sprintf(" · %ds", int(d.Round(time.Second).Seconds()))
					}
				}
				out := []string{summary}

				// "by model" table: this session's parent turns plus
				// every dispatched subagent's usage, broken out by
				// whichever model it actually ran on — a session that
				// dispatched to a cheaper role should be able to see
				// that split, not just one blended number.
				if len(byModel) > 1 {
					names := make([]string, 0, len(byModel))
					for name := range byModel {
						names = append(names, name)
					}
					sort.Strings(names)
					out = append(out, "", "by model:")
					for _, name := range names {
						u := byModel[name]
						cost := "-"
						if u.Cost.Total != 0 {
							cost = fmt.Sprintf("$%.4f", u.Cost.Total)
						}
						out = append(out, fmt.Sprintf("  %-24s input %-8d output %-8d cost %s", name, u.Input, u.Output, cost))
					}
				}
				return Result{Output: out}, nil
			},
		},
		{
			Name:         "model",
			Description:  "Switch model",
			ArgumentHint: "<provider/model>",
			ArgumentCompletions: func(prefix string) []Completion {
				models, err := cache.get(context.Background(), deps.Registry)
				if err != nil {
					return nil
				}
				wanted := strings.ToLower(strings.TrimSpace(prefix))
				var out []Completion
				for _, m := range models {
					id := modelID(m)
					if wanted != "" && !strings.Contains(strings.ToLower(id), wanted) {
						continue
					}
					out = append(out, Completion{Value: id, Label: id, Description: modelDescription(m)})
				}
				return out
			},
			Run: func(ctx context.Context, args string) (Result, error) {
				apply := func(target string) (string, error) {
					providerID, mID, ok := splitProviderModel(target)
					if !ok {
						return "", fmt.Errorf(`"%s" is not provider/model`, target)
					}
					if deps.SwitchModel == nil {
						return "", fmt.Errorf("model switching is not wired up")
					}
					tier, err := deps.SwitchModel(ctx, providerID, mID)
					if err != nil {
						return "", err
					}
					label := providerID + "/" + mID
					if deps.OnModelChanged != nil {
						deps.OnModelChanged(label, tier)
					}
					return fmt.Sprintf("now on %s — %s tier, %s usable", label, tier.Name, formatTokens(budget.UsableTokens(tier))), nil
				}

				trimmed := strings.TrimSpace(args)
				if trimmed == "roles" {
					models, err := cache.get(ctx, deps.Registry)
					if err != nil {
						models = nil
					}
					return Result{Output: modelRolesTable(deps.ModelRoles, models)}, nil
				}
				if trimmed != "" {
					msgOut, err := apply(trimmed)
					if err != nil {
						return Result{}, err
					}
					return Result{Output: []string{msgOut}}, nil
				}

				curProvider, curModel := deps.CurrentModel()
				currentID := ""
				if curProvider != "" {
					currentID = curProvider + "/" + curModel
				}

				models, err := cache.get(ctx, deps.Registry)
				if err != nil {
					models = nil
				}
				// models is already provider-grouped: modelCache.get
				// appends p.Models() per provider in reg.Available's
				// order, so numbering it in place reproduces /model's
				// "grouped by provider via ordering, no section headers"
				// contract with no extra sort here.
				items := make([]Item, 0, len(models))
				lines := []string{fmt.Sprintf("current: %s", orUnknown(currentID)), ""}
				for i, m := range models {
					id := modelID(m)
					it := Item{
						Value:       id,
						Label:       fmt.Sprintf("%d. %s", i+1, id),
						Description: modelDialogDescription(m),
					}
					if id == currentID {
						it.Marker = "✔"
					}
					items = append(items, it)
					lines = append(lines, "  "+id)
				}

				modal := &ModalSpec{
					Title:  "Model",
					Kind:   "model",
					Header: []string{fmt.Sprintf("current    %s", orUnknown(currentID))},
					Items:  items,
					// Effort is a static "Medium" label: the harness has
					// no reasoning-effort concept anywhere else in the
					// codebase (no field on provider.Model, no setting,
					// nothing budget/tier tracks), so there is nothing
					// real to report or adjust. SetEffort is left nil,
					// which internal/tui's dialogModel renders as a
					// static row (←/→ a no-op) — see the handback
					// report.
					Effort: "Medium",
					Select: func(value string) (string, error) {
						providerID, mID, ok := splitProviderModel(value)
						if !ok {
							return "", fmt.Errorf(`"%s" is not provider/model`, value)
						}
						return apply(providerID + "/" + mID)
					},
					SelectDefault: func(value string) (string, error) {
						providerID, mID, ok := splitProviderModel(value)
						if !ok {
							return "", fmt.Errorf(`"%s" is not provider/model`, value)
						}
						if _, err := apply(providerID + "/" + mID); err != nil {
							return "", err
						}
						label := providerID + "/" + mID
						if err := writesettings.SetUserModel(label); err != nil {
							return "", fmt.Errorf("switched but could not persist default: %w", err)
						}
						// No leading "⎿ " glyph: this package cannot import
						// internal/tui to route it through the plain-mode
						// glyph table (BuiltinDeps's own doc comment: no
						// cli/tui import, only callbacks), and every other
						// status string this file returns is plain text —
						// dialog_model.go's Render already colours and
						// places this as the dialog's status line, so the
						// glyph was redundant decoration, and a hardcoded
						// one leaked a box-drawing character into plain/
						// screen-reader mode (defect *screen-reader-mode-
						// leaves-box-drawing-rules).
						return fmt.Sprintf("Model set to %s (default for new sessions)", label), nil
					},
				}
				return Result{Output: lines, Modal: modal}, nil
			},
		},
		{
			Name:        "tools",
			Description: "Show which tools are currently resident",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.Lane == nil {
					return Result{Output: []string{"0 resident:"}}, nil
				}
				active, err := deps.Lane.GetActiveTools()
				if err != nil {
					return Result{}, err
				}
				lines := []string{fmt.Sprintf("%d resident:", len(active))}
				for _, t := range active {
					lines = append(lines, "  "+t)
				}
				return Result{Output: lines}, nil
			},
		},
		{
			Name:        "agents",
			Description: "Manage subagents",
			Run: func(ctx context.Context, args string) (Result, error) {
				if len(deps.Agents) == 0 {
					return Result{Output: []string{"No subagents. Define them in .claude/agents/*.md"}}, nil
				}
				lines := []string{fmt.Sprintf("%d available:", len(deps.Agents))}
				for _, a := range deps.Agents {
					model := ""
					if a.Model != "" {
						model = fmt.Sprintf(" (%s)", a.Model)
					}
					tools := ""
					if a.Tools != nil {
						tools = fmt.Sprintf(" [%d tools]", len(a.Tools))
					}
					lines = append(lines, fmt.Sprintf("  %s%s%s\n    %s", a.Name, model, tools, a.Description))
				}
				return Result{Output: lines}, nil
			},
		},
		{
			Name:        "status",
			Description: "Show model, provider and connection status",
			Run: func(ctx context.Context, args string) (Result, error) {
				providerID, mID := deps.CurrentModel()
				auth := "none"
				if deps.Registry != nil && providerID != "" {
					ok, err := deps.Registry.CheckAuth(ctx, providerID)
					if err == nil && ok {
						auth = "configured"
					}
				}
				t := deps.CurrentTier()
				return Result{Output: []string{
					fmt.Sprintf("model     %s/%s", providerID, mID),
					fmt.Sprintf("auth      %s", auth),
					fmt.Sprintf("tier      %s", t.Name),
					fmt.Sprintf("sessions  %s", deps.SessionsDir),
				}}, nil
			},
		},
		{
			Name:        "exit",
			Description: "Exit the harness",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.OnExit != nil {
					deps.OnExit()
				}
				return Result{Exit: true}, nil
			},
		},
		{
			Name:        "quit",
			Description: "Exit the harness",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.OnExit != nil {
					deps.OnExit()
				}
				return Result{Exit: true}, nil
			},
		},
	}

	return StaticSource(OriginBuiltin, cmds)
}

// BindHelp fills in /help's Run with a listing of every command the given
// registry resolves to. /help needs the registry that contains it, so it
// is bound after construction rather than capturing a half-built
// reference.
func BindHelp(source Source, list func() []Command) Source {
	return Source{
		Origin: source.Origin,
		Load: func() ([]Command, error) {
			cmds, err := source.Load()
			if err != nil {
				return nil, err
			}
			out := make([]Command, len(cmds))
			for i, c := range cmds {
				if c.Name == "help" {
					c.Run = func(ctx context.Context, args string) (Result, error) {
						lines := make([]string, 0)
						for _, cc := range list() {
							hint := ""
							if cc.ArgumentHint != "" {
								hint = " " + cc.ArgumentHint
							}
							lines = append(lines, fmt.Sprintf("  /%s%s  %s", QualifiedName(cc), hint, cc.Description))
						}
						return Result{Output: lines}, nil
					}
				}
				out[i] = c
			}
			return out, nil
		},
	}
}

// splitProviderModel splits "provider/model" into its two parts. A model
// id may itself contain slashes (e.g. openrouter's "org/name"), so only the
// first slash is significant.
func splitProviderModel(target string) (providerID, modelID string, ok bool) {
	for i := 0; i < len(target); i++ {
		if target[i] == '/' {
			providerID = target[:i]
			modelID = target[i+1:]
			return providerID, modelID, providerID != "" && modelID != ""
		}
	}
	return "", "", false
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
