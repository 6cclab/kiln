import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import {
	ContextTooSmallError,
	requireTierForWindow,
	strategyForWindow,
	TOOL_STRATEGY_COST,
	tierForWindow,
	usableTokens,
} from "../src/budget/tier.ts";

/**
 * Budget tiers.
 *
 * The numbers here are anchored to real measurements (see
 * phase0/out/results.json): 165 MCP tools cost 31,897 tokens as full schemas
 * against a 32,768-token window. These tests pin the consequences of that.
 */
describe("strategyForWindow", () => {
	it("refuses full schemas on a small window", () => {
		// 31,897 tokens is 97% of 32k — the measurement this whole design exists for.
		assert.notEqual(strategyForWindow(32_768), "full-schemas");
	});

	it("allows full schemas on a large window", () => {
		assert.equal(strategyForWindow(200_000), "full-schemas");
	});

	it("never returns a strategy costing more than the 20% share", () => {
		for (const window of [8_192, 32_768, 49_152, 131_072, 200_000, 1_000_000]) {
			const cost = TOOL_STRATEGY_COST[strategyForWindow(window)];
			// posture-index is the floor; below that there is nothing cheaper to pick.
			if (strategyForWindow(window) !== "posture-index") {
				assert.ok(cost <= window * 0.2, `${window}: ${cost} exceeds share`);
			}
		}
	});
});

describe("tierForWindow", () => {
	it("keeps pi's compaction defaults from overflowing a 32k window", () => {
		// pi's defaults are reserve 16384 + keepRecent 20000 = 36k, which does not
		// fit 32,768 at all. This is the specific bug the tier system replaces.
		const tier = tierForWindow(32_768);
		assert.ok(tier.compaction.reserveTokens + tier.compaction.keepRecentTokens < 32_768);
	});

	it("assigns tiers by window size", () => {
		assert.equal(tierForWindow(32_768).name, "small");
		assert.equal(tierForWindow(49_152).name, "medium");
		assert.equal(tierForWindow(200_000).name, "large");
	});

	it("leaves real conversation room on every runnable window", () => {
		for (const window of [16_384, 32_768, 49_152, 200_000]) {
			assert.ok(usableTokens(tierForWindow(window)) > 0, `${window} unusable`);
		}
	});
});

describe("requireTierForWindow", () => {
	it("throws rather than returning a tier that cannot run", () => {
		// Ollama's own default is 4096. Returning a tier here would surface later
		// as a mysteriously truncated first turn instead of a clear error.
		assert.throws(() => requireTierForWindow(4_096), ContextTooSmallError);
	});

	it("reports how far short the window falls", () => {
		try {
			requireTierForWindow(4_096);
			assert.fail("should have thrown");
		} catch (err) {
			assert.ok(err instanceof ContextTooSmallError);
			assert.ok(err.shortfall > 0);
		}
	});

	it("accepts a window that fits", () => {
		assert.equal(requireTierForWindow(32_768).name, "small");
	});
});
