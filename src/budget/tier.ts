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

function clamp(value: number, min: number, max: number): number {
	return Math.max(min, Math.min(max, Math.round(value)));
}

/**
 * Share of the window each budget may take.
 *
 * Fractions, not constants. An earlier version used absolute token counts per
 * tier, which produced a real and invisible fault: a 49,152-token window fell
 * into `medium`, inherited budgets sized for 128k, and ended up with **less
 * usable context (21,075) than a 32,768-token window (25,338)**. A bigger model
 * was worse to use than a smaller one, and nothing surfaced it.
 *
 * Deriving from the window makes the budgets monotonic by construction: more
 * window is always more room.
 */
const SHARE = {
	/** System prompt, including CLAUDE.md. */
	systemPrompt: 0.1,
	/** Headroom kept free so a reply always has somewhere to land. */
	reserve: 0.1,
	/** What compaction keeps verbatim. */
	keepRecent: 0.25,
	/** Ceiling for ONE tool result. */
	toolOutput: 0.12,
} as const;

/**
 * Resolve the operating posture for a context window.
 *
 * The tier NAME is a step function because the thing it selects genuinely is
 * one: the tool catalog has a fixed cost, so "can I afford full schemas" flips
 * at a threshold. The budgets are not — they scale with the window.
 */
export function tierForWindow(contextWindow: number): Tier {
	const toolStrategy = strategyForWindow(contextWindow);
	const name = contextWindow <= 32_768 ? "small" : contextWindow <= 131_072 ? "medium" : "large";

	/**
	 * Floors keep a tiny window from budgeting a few hundred tokens for a system
	 * prompt; ceilings keep a 1M window from reserving 100k it will never need.
	 *
	 * The floor is itself capped at the share it protects. Without that, a floor
	 * meant to be generous becomes the dominant cost on a small window — at
	 * 8,192 tokens, three 2,048 floors claimed 75% of the window before a single
	 * message existed.
	 */
	const budget = (share: number, floor: number, ceiling: number): number =>
		clamp(contextWindow * share, Math.min(floor, Math.floor(contextWindow * share * 1.25)), ceiling);

	const systemPromptTokens = budget(SHARE.systemPrompt, 2_048, 32_768);
	const reserveTokens = budget(SHARE.reserve, 2_048, 32_768);
	const keepRecentTokens = budget(SHARE.keepRecent, 4_096, 100_000);
	const toolOutputTokens = budget(SHARE.toolOutput, 2_048, 49_152);

	return {
		name,
		contextWindow,
		// pi's defaults (reserve 16384 + keepRecent 20000) sum to 36k and would
		// overflow a 32k window before a single message is added, which is why
		// these are derived rather than inherited.
		compaction: { enabled: true, reserveTokens, keepRecentTokens },
		toolStrategy,
		systemPromptTokens,
		toolOutputTokens,
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
/**
 * The least usable room a session can do anything with.
 *
 * `usable > 0` was the old bar, and it stopped meaning anything once budgets
 * became proportional: every window leaves *something*, so a 4,096-token model
 * passed with 1,786 tokens of room — enough for a system prompt and nothing
 * else. The bar has to be "can one real turn happen", not "is the arithmetic
 * positive": a question, one file read, and a reply.
 */
export const MIN_USABLE_TOKENS = 4_096;

export function requireTierForWindow(contextWindow: number): Tier {
	const tier = tierForWindow(contextWindow);
	const usable = usableTokens(tier);
	if (usable < MIN_USABLE_TOKENS) throw new ContextTooSmallError(tier, MIN_USABLE_TOKENS - usable);
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
