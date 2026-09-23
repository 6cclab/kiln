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

describe("budgets scale with the window", () => {
	/**
	 * The fault this prevents, which was live and invisible:
	 *
	 *   49,152-token window -> 21,075 usable
	 *   32,768-token window -> 25,338 usable
	 *
	 * A bigger model was worse to use than a smaller one, because the tiers held
	 * absolute token counts and 49k inherited budgets sized for 128k.
	 */
	// Starts at the smallest window the harness will actually accept; below that
	// `requireTierForWindow` refuses, so budgets there are not a promise.
	const WINDOWS = [8_192, 16_384, 32_768, 49_152, 65_536, 128_000, 200_000, 1_000_000];

	it("never gives a larger window less usable room", () => {
		let previous = -1;
		for (const window of WINDOWS) {
			const usable = usableTokens(tierForWindow(window));
			assert.ok(usable > previous, `${window} gave ${usable}, less than the window below it`);
			previous = usable;
		}
	});

	it("keeps one tool result from dominating the window", () => {
		// At 33% of a 49k window, reading two files ended the conversation.
		for (const window of WINDOWS) {
			const tier = tierForWindow(window);
			assert.ok(
				tier.toolOutputTokens <= window * 0.15,
				`${window}: one tool result may take ${Math.round((tier.toolOutputTokens / window) * 100)}%`,
			);
		}
	});

	it("leaves the reply somewhere to land, as a share of the window", () => {
		// Asserted as a share, not an absolute: 2k is generous at 200k and half
		// the window at 8k, so an absolute floor is the thing that broke this.
		for (const window of WINDOWS) {
			const reserve = tierForWindow(window).compaction.reserveTokens;
			const share = reserve / window;
			assert.ok(share >= 0.03, `${window}: reserve is only ${Math.round(share * 100)}% of the window`);
			assert.ok(share <= 0.2, `${window}: reserve eats ${Math.round(share * 100)}% of the window`);
		}
	});

	it("never budgets more than the window holds", () => {
		for (const window of WINDOWS) {
			const tier = tierForWindow(window);
			const floor = tier.systemPromptTokens + tier.compaction.reserveTokens;
			assert.ok(floor < window, `${window}: fixed costs (${floor}) exceed the window`);
		}
	});

	it("still picks the tool strategy by threshold, which is a real step", () => {
		// The catalog has a fixed cost, so affordability genuinely flips.
		assert.equal(tierForWindow(32_768).toolStrategy, "posture-index");
		assert.equal(tierForWindow(49_152).toolStrategy, "full-index");
		assert.equal(tierForWindow(200_000).toolStrategy, "full-schemas");
	});
});
