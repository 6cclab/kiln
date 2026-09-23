import { DEFAULT_COMPACTION_SETTINGS, type CompactionSettings } from "@earendil-works/pi-agent-core";
import type { Model } from "@earendil-works/pi-ai";

/**
 * Context budgeting.
 *
 * This is the only module that turns "which model am I on" into behavior.
 * Everything downstream reads a Tier and never asks about the provider, which
 * is what keeps the harness model-agnostic: the same code path relaxes on a
 * 200k subscription model instead of branching to a different one.
 *
 * The numbers are anchored to measurements taken 2026-09-22 against the live
 * MCP catalog (165 tools across 9 servers). See phase0/out/results.json.
 */

/** How the MCP tool catalog is presented to the model. */
export type ToolStrategy =
	/** Only in-posture servers are indexed, one `name: description` line each. Measured 1,286 tokens. */
	| "posture-index"
	/** Every server indexed, still one line each, no schemas. Measured 7,597 tokens. */
	| "full-index"
	/** Every tool's complete JSON Schema is resident. Measured 31,897 tokens. */
	| "full-schemas";

export interface Tier {
	name: "small" | "medium" | "large";
	/** The window this tier was resolved from. */
	contextWindow: number;
	compaction: CompactionSettings;
	toolStrategy: ToolStrategy;
	/** Ceiling for the assembled system prompt, including CLAUDE.md content. */
	systemPromptTokens: number;
	/** Ceiling for a single tool result before it is truncated. */
	toolOutputTokens: number;
}

/**
 * Measured cost of each tool strategy, in tokens, via Ollama's
 * `prompt_eval_count` on qwen3-cc. Exported so the TUI footer and the tier
 * chooser agree on one set of numbers rather than each carrying its own copy.
 */
export const TOOL_STRATEGY_COST: Record<ToolStrategy, number> = {
	"posture-index": 1_286,
	"full-index": 7_597,
	"full-schemas": 31_897,
};

/**
 * Share of the window the tool catalog may occupy.
 *
 * The strategy is derived from this rather than hard-coded per tier. Deriving it
 * matters because window size and tier name are not the same question: a 48k
 * local model and a 48k hosted model want the same catalog treatment, and a new
 * strategy added to the table below is picked up automatically at every size.
 */
const TOOL_BUDGET_SHARE = 0.2;

/** Most generous strategy whose measured cost fits the share. Falls back to the cheapest. */
export function strategyForWindow(contextWindow: number): ToolStrategy {
	const ceiling = contextWindow * TOOL_BUDGET_SHARE;
	const affordable = (["full-schemas", "full-index", "posture-index"] as const).find(
		(s) => TOOL_STRATEGY_COST[s] <= ceiling,
	);
	return affordable ?? "posture-index";
}

/**
 * Resolve the operating posture for a context window.
 *
 * Boundaries are driven by measurement rather than chosen for roundness: full
 * schemas are 97% of a 32k window but only ~16% of a 200k one, so the strategy
 * that is reckless on a local Qwen is unremarkable on Claude.
 */
export function tierForWindow(contextWindow: number): Tier {
	const toolStrategy = strategyForWindow(contextWindow);

	if (contextWindow <= 32_768) {
		return {
			name: "small",
			contextWindow,
			// pi's defaults (reserve 16384 + keepRecent 20000) sum to 36k and
			// overflow a 32k window before a single message is added.
			compaction: { enabled: true, reserveTokens: 4_096, keepRecentTokens: 8_192 },
			toolStrategy,
			systemPromptTokens: 2_048,
			toolOutputTokens: 4_096,
		};
	}
	if (contextWindow <= 131_072) {
		return {
			name: "medium",
			contextWindow,
			compaction: { enabled: true, reserveTokens: 12_288, keepRecentTokens: 24_576 },
			toolStrategy,
			systemPromptTokens: 8_192,
			toolOutputTokens: 16_384,
		};
	}
	return {
		name: "large",
		contextWindow,
		compaction: { ...DEFAULT_COMPACTION_SETTINGS },
		toolStrategy,
		systemPromptTokens: 24_576,
		toolOutputTokens: 49_152,
	};
}

/** Raised when a model's window cannot fit the harness's own floor. */
export class ContextTooSmallError extends Error {
	// Explicit fields, not constructor parameter properties: the repo runs on
	// Node's strip-only type erasure, which rejects parameter properties.
	tier: Tier;
	shortfall: number;

	constructor(tier: Tier, shortfall: number) {
		super(
			`Context window of ${tier.contextWindow} tokens is too small: the ${tier.name} tier floor ` +
				`(system prompt ${tier.systemPromptTokens} + tools ${TOOL_STRATEGY_COST[tier.toolStrategy]} + ` +
				`reserve ${tier.compaction.reserveTokens}) exceeds it by ${shortfall} tokens. ` +
				`Raise num_ctx, or pick a model with a larger window.`,
		);
		this.name = "ContextTooSmallError";
		this.tier = tier;
		this.shortfall = shortfall;
	}
}

/**
 * Resolve a tier, refusing rather than returning one that cannot run.
 *
 * Use this at startup. A negative budget otherwise shows up as a mysteriously
 * truncated first turn, which is a much worse way to learn the same fact.
 */
export function requireTierForWindow(contextWindow: number): Tier {
	const tier = tierForWindow(contextWindow);
	const usable = usableTokens(tier);
	if (usable <= 0) throw new ContextTooSmallError(tier, -usable);
	return tier;
}

/** Resolve a tier straight from a pi model. Every provider populates `contextWindow`. */
export function tierFor(model: Pick<Model<never>, "contextWindow">): Tier {
	return tierForWindow(model.contextWindow);
}

/**
 * Tokens left for conversation after the tier's fixed overheads.
 *
 * Negative means the configuration cannot run: the floor alone exceeds the
 * window. Surfacing that as a number beats discovering it as a truncated
 * first turn.
 */
export function usableTokens(tier: Tier): number {
	return (
		tier.contextWindow -
		tier.systemPromptTokens -
		TOOL_STRATEGY_COST[tier.toolStrategy] -
		tier.compaction.reserveTokens
	);
}
