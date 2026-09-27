// Package budget turns "which model am I on" into operating behavior.
//
// This is a verbatim port of harness/src/budget/tier.ts. Everything
// downstream reads a Tier and never asks about the provider, which is what
// keeps the harness model-agnostic: the same code path relaxes on a 200k
// subscription model instead of branching to a different one.
//
// The numbers are anchored to measurements taken 2026-09-22 against the live
// MCP catalog (165 tools across 9 servers). See phase0/out/results.json in
// the TS reference repo.
package budget

import (
	"fmt"
	"math"
)

// ToolStrategy is how the MCP tool catalog is presented to the model.
type ToolStrategy string

const (
	// StrategyPostureIndex indexes only in-posture servers, one
	// "name: description" line each. Measured 1,286 tokens.
	StrategyPostureIndex ToolStrategy = "posture-index"
	// StrategyFullIndex indexes every server, still one line each, no
	// schemas. Measured 7,597 tokens.
	StrategyFullIndex ToolStrategy = "full-index"
	// StrategyFullSchemas keeps every tool's complete JSON Schema resident.
	// Measured 31,897 tokens.
	StrategyFullSchemas ToolStrategy = "full-schemas"
)

// CompactionSettings mirrors pi-agent-core's CompactionSettings, the subset
// the tier computes.
type CompactionSettings struct {
	Enabled          bool
	ReserveTokens    int
	KeepRecentTokens int
}

// Tier is the operating posture derived from a model's context window.
type Tier struct {
	// Name is "small", "medium" or "large".
	Name string
	// ContextWindow is the window this tier was resolved from.
	ContextWindow int
	Compaction    CompactionSettings
	ToolStrategy  ToolStrategy
	// SystemPromptTokens is the ceiling for the assembled system prompt,
	// including CLAUDE.md content.
	SystemPromptTokens int
	// ToolOutputTokens is the ceiling for a single tool result before it is
	// truncated.
	ToolOutputTokens int
}

// TOOL_STRATEGY_COST measured cost of each tool strategy, in tokens, via
// Ollama's prompt_eval_count on qwen3-cc. Exported so the TUI footer and the
// tier chooser agree on one set of numbers rather than each carrying its own
// copy.
var ToolStrategyCost = map[ToolStrategy]int{
	StrategyPostureIndex: 1_286,
	StrategyFullIndex:    7_597,
	StrategyFullSchemas:  31_897,
}

// StrategyForWindow always returns posture-index, regardless of window
// size: MCP schemas stay behind tool_search, and the prompt indexes only the
// active posture's servers. A window having room for resident schemas does
// not make them worth paying for: on a paid API they are token cost and
// latency on every turn, while tool_search admits a tool only when the model
// needs it. On a 165-tool catalog and a 1M-window model, resident schemas
// made the first request 74k tokens and the session 3x the cost of a
// tool_search-based agent. full-index and full-schemas stay defined and
// priced (ToolStrategyCost) for the code that reasons about their cost.
func StrategyForWindow(contextWindow int) ToolStrategy {
	return StrategyPostureIndex
}

func clamp(value float64, min, max int) int {
	v := int(math.Round(value))
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return v
}

// share is the fraction of the window each budget may take.
//
// Fractions, not constants. An earlier version used absolute token counts per
// tier, which produced a real and invisible fault: a 49,152-token window fell
// into "medium", inherited budgets sized for 128k, and ended up with less
// usable context (21,075) than a 32,768-token window (25,338). A bigger model
// was worse to use than a smaller one, and nothing surfaced it.
//
// Deriving from the window makes the budgets monotonic by construction: more
// window is always more room.
const (
	shareSystemPrompt = 0.1
	shareReserve      = 0.1
	shareKeepRecent   = 0.25
	shareToolOutput   = 0.12
)

// TierForWindow resolves the operating posture for a context window.
//
// The tier NAME is still a step function -- small/medium/large budgets
// genuinely are different regimes -- but it no longer has anything to do
// with the tool strategy: ToolStrategy is posture-index at every size (see
// StrategyForWindow). The budgets scale with the window regardless of name.
func TierForWindow(contextWindow int) Tier {
	toolStrategy := StrategyForWindow(contextWindow)
	var name string
	switch {
	case contextWindow <= 32_768:
		name = "small"
	case contextWindow <= 131_072:
		name = "medium"
	default:
		name = "large"
	}

	// Floors keep a tiny window from budgeting a few hundred tokens for a
	// system prompt; ceilings keep a 1M window from reserving 100k it will
	// never need.
	//
	// The floor is itself capped at the share it protects. Without that, a
	// floor meant to be generous becomes the dominant cost on a small
	// window -- at 8,192 tokens, three 2,048 floors claimed 75% of the
	// window before a single message existed.
	budget := func(share float64, floor, ceiling int) int {
		cappedFloor := floor
		if alt := int(math.Floor(float64(contextWindow) * share * 1.25)); alt < cappedFloor {
			cappedFloor = alt
		}
		return clamp(float64(contextWindow)*share, cappedFloor, ceiling)
	}

	systemPromptTokens := budget(shareSystemPrompt, 2_048, 32_768)
	reserveTokens := budget(shareReserve, 2_048, 32_768)
	keepRecentTokens := budget(shareKeepRecent, 4_096, 100_000)
	toolOutputTokens := budget(shareToolOutput, 2_048, 49_152)

	return Tier{
		Name:          name,
		ContextWindow: contextWindow,
		// pi's defaults (reserve 16384 + keepRecent 20000) sum to 36k and
		// would overflow a 32k window before a single message is added,
		// which is why these are derived rather than inherited.
		Compaction: CompactionSettings{
			Enabled:          true,
			ReserveTokens:    reserveTokens,
			KeepRecentTokens: keepRecentTokens,
		},
		ToolStrategy:       toolStrategy,
		SystemPromptTokens: systemPromptTokens,
		ToolOutputTokens:   toolOutputTokens,
	}
}

// ContextTooSmallError is raised when a model's window cannot fit the
// harness's own floor.
type ContextTooSmallError struct {
	Tier      Tier
	Shortfall int
}

func (e *ContextTooSmallError) Error() string {
	return fmt.Sprintf(
		"Context window of %d tokens is too small: the %s tier floor "+
			"(system prompt %d + tools %d + reserve %d) exceeds it by %d tokens. "+
			"Raise num_ctx, or pick a model with a larger window.",
		e.Tier.ContextWindow, e.Tier.Name, e.Tier.SystemPromptTokens,
		ToolStrategyCost[e.Tier.ToolStrategy], e.Tier.Compaction.ReserveTokens, e.Shortfall,
	)
}

// MinUsableTokens is the least usable room a session can do anything with.
//
// usable > 0 was the old bar, and it stopped meaning anything once budgets
// became proportional: every window leaves *something*, so a 4,096-token
// model passed with 1,786 tokens of room -- enough for a system prompt and
// nothing else. The bar has to be "can one real turn happen", not "is the
// arithmetic positive": a question, one file read, and a reply.
const MinUsableTokens = 4_096

// RequireTierForWindow resolves a tier, refusing rather than returning one
// that cannot run.
//
// Use this at startup. A negative budget otherwise shows up as a
// mysteriously truncated first turn, which is a much worse way to learn the
// same fact.
func RequireTierForWindow(contextWindow int) (Tier, error) {
	tier := TierForWindow(contextWindow)
	usable := UsableTokens(tier)
	if usable < MinUsableTokens {
		return tier, &ContextTooSmallError{Tier: tier, Shortfall: MinUsableTokens - usable}
	}
	return tier, nil
}

// TierFor resolves a tier straight from a model's context window. Every
// provider populates ContextWindow.
func TierFor(contextWindow int) Tier {
	return TierForWindow(contextWindow)
}

// UsableTokens returns the tokens left for conversation after the tier's
// fixed overheads.
//
// Negative means the configuration cannot run: the floor alone exceeds the
// window. Surfacing that as a number beats discovering it as a truncated
// first turn.
func UsableTokens(tier Tier) int {
	return tier.ContextWindow - tier.SystemPromptTokens - ToolStrategyCost[tier.ToolStrategy] - tier.Compaction.ReserveTokens
}
