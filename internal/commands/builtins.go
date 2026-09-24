package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/harness"
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

	// CurrentModel returns the active provider and model id.
	CurrentModel func() (providerID, modelID string)
	// CurrentTier returns the tier currently in effect.
	CurrentTier func() budget.Tier
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

	// SessionsDir is shown by /status.
	SessionsDir string

	OnClear func()
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

// BuiltinCommands returns the source for the ten commands builtins.ts
// registers (help is filled in by BindHelp once the owning registry
// exists).
func BuiltinCommands(deps BuiltinDeps) Source {
	cache := &modelCache{}

	cmds := []Command{
		{
			Name:        "help",
			Description: "Show available commands",
			Run: func(ctx context.Context, args string) (Result, error) {
				return Result{}, nil // filled in by BindHelp
			},
		},
		{
			Name:        "clear",
			Description: "Clear conversation history",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.OnClear != nil {
					deps.OnClear()
				}
				return Result{Output: []string{"Conversation cleared."}}, nil
			},
		},
		{
			Name:        "compact",
			Description: "Summarize and compact the current context",
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
			Description: "Show context usage against the active tier",
			Run: func(ctx context.Context, args string) (Result, error) {
				t := deps.CurrentTier()
				return Result{Output: []string{
					fmt.Sprintf("window        %d", t.ContextWindow),
					fmt.Sprintf("tier          %s", t.Name),
					fmt.Sprintf("tools         %s (%d tokens)", t.ToolStrategy, budget.ToolStrategyCost[t.ToolStrategy]),
					fmt.Sprintf("system prompt %d max", t.SystemPromptTokens),
					fmt.Sprintf("reserved      %d", t.Compaction.ReserveTokens),
					fmt.Sprintf("available     %d for conversation", budget.UsableTokens(t)),
				}}, nil
			},
		},
		{
			Name:        "cost",
			Description: "Show token usage and cost for this session",
			Run: func(ctx context.Context, args string) (Result, error) {
				providerID, modelID := deps.CurrentModel()
				m, ok := deps.Registry.GetModel(providerID, modelID)
				if !ok {
					return Result{Output: []string{"No cost data for this model."}}, nil
				}
				rate := m.Cost.ModelCostRates
				line := fmt.Sprintf("input $%g/M · output $%g/M", rate.Input, rate.Output)
				if rate.Input == 0 {
					line += "  (self-hosted, no marginal cost)"
				}
				return Result{Output: []string{line}}, nil
			},
		},
		{
			Name:         "model",
			Description:  "Show or change the active model",
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
				items := make([]Item, 0, len(models))
				lines := []string{fmt.Sprintf("current: %s", orUnknown(currentID)), ""}
				for _, m := range models {
					id := modelID(m)
					label := id
					if id == currentID {
						label = id + "  ←"
					}
					items = append(items, Item{Value: id, Label: label, Description: modelDescription(m)})
					lines = append(lines, "  "+id)
				}

				modal := &ModalSpec{
					Title:  "Model",
					Header: []string{fmt.Sprintf("current    %s", orUnknown(currentID))},
					Items:  items,
					Select: func(value string) (string, error) {
						providerID, mID, ok := splitProviderModel(value)
						if !ok {
							return "", fmt.Errorf(`"%s" is not provider/model`, value)
						}
						return apply(providerID + "/" + mID)
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
			Description: "Show the subagents available for dispatch",
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
				return Result{}, nil
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
