import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { builtinCommands } from "../src/commands/builtins.ts";
import { formatTokens } from "../src/tui/transcript.ts";
import { tierForWindow } from "../src/budget/tier.ts";

/**
 * Argument completions.
 *
 * `/model` required typing an exact `provider/model` id, which on a local
 * Ollama host means remembering tag strings like `qwen3:30b-a3b`. The list is
 * the feature; the command was only half of it.
 *
 * `Command extends SlashCommand`, so pi-tui picks `getArgumentCompletions` up
 * automatically — the hook was in the type all along and simply never
 * implemented.
 */

const MODELS = [
	{ provider: "ollama", id: "qwen3.8:latest", contextWindow: 49_152 },
	{ provider: "ollama", id: "qwen3-cc:latest", contextWindow: 32_768 },
	{ provider: "anthropic", id: "claude-opus-5", contextWindow: 1_000_000 },
];

function modelCommand() {
	const registry = {
		available: async () => MODELS,
		models: {},
		login: async () => undefined,
		resolve: async () => undefined,
	} as never;
	const source = builtinCommands({
		session: { tier: {}, lane: {}, sessionsDir: "" } as never,
		registry,
		onClear: () => {},
		onExit: () => {},
	});
	return source.load();
}

describe("/model completions", () => {
	const get = async (prefix: string) => {
		const model = (await modelCommand()).find((c) => c.name === "model");
		assert.ok(model?.getArgumentCompletions, "no completions on /model");
		return (await model.getArgumentCompletions(prefix)) ?? [];
	};

	it("offers every reachable model when nothing is typed", async () => {
		assert.equal((await get("")).length, MODELS.length);
	});

	it("filters on any part of the id, not just the start", async () => {
		// You remember "qwen3.8", not "ollama/qwen3.8:latest".
		const items = await get("qwen3.8");
		assert.equal(items.length, 1);
		assert.equal(items[0].value, "ollama/qwen3.8:latest");
	});

	it("filters by provider too", async () => {
		assert.equal((await get("anthropic")).length, 1);
	});

	it("is case-insensitive", async () => {
		assert.equal((await get("QWEN3.8")).length, 1);
	});

	it("completes to a value the command can actually parse", async () => {
		// The command splits on "/", so the completion has to produce that shape.
		for (const item of await get("")) {
			assert.ok(item.value.includes("/"), `"${item.value}" is not provider/model`);
		}
	});

	it("says what differs between them, which is what decides the choice", async () => {
		const description = (await get("qwen3.8"))[0].description ?? "";
		assert.ok(description.includes("49.2k"), description);
		assert.ok(description.includes("medium"), description);
		assert.ok(description.includes("usable"), description);
	});

	it("returns nothing for a prefix that matches nothing", async () => {
		assert.deepEqual(await get("nonexistent-model"), []);
	});
});

describe("formatTokens", () => {
	it("uses millions for million-token windows", () => {
		// "1000.0k" reads as a mistake, and million-token windows are ordinary.
		assert.equal(formatTokens(1_000_000), "1.0m");
		assert.equal(formatTokens(200_000), "200.0k");
		assert.equal(formatTokens(49_152), "49.2k");
		assert.equal(formatTokens(999), "999");
	});
});

describe("/model switching moves the whole posture", () => {
	/**
	 * `setModel` changes one lane setting. The tier is the single place model
	 * choice becomes behavior — compaction, tool strategy, per-result budgets —
	 * and none of it moved, so selecting a 1M model left the session running on a
	 * 49k budget, and the reverse left a tool catalog loaded that no longer fit.
	 *
	 * Selecting a model and seeing nothing change is also indistinguishable from
	 * it not working, which is how this was reported.
	 */
	function stub() {
		let model = { provider: "ollama", id: "qwen3.8:latest" };
		let compaction: unknown = null;
		const changes: Array<{ label: string; tier: { name: string } }> = [];
		const session = {
			tier: tierForWindow(49_152),
			harness: {
				setCompactionSettings: async (c: unknown) => {
					compaction = c;
				},
			},
			lane: {
				getModel: async () => model,
				setModel: async (x: { provider: string; modelId: string }) => {
					model = { provider: x.provider, id: x.modelId };
				},
			},
			sessionsDir: "",
		};
		const registry = {
			available: async () => [
				{ provider: "ollama", id: "qwen3.8:latest", contextWindow: 49_152 },
				{ provider: "anthropic", id: "claude-opus-5", contextWindow: 1_000_000 },
			],
			resolve: async (_p: string, m: string) => ({ tier: tierForWindow(m.includes("opus") ? 1_000_000 : 49_152) }),
			models: {},
		};
		return { session, registry, changes, getModel: () => model, getCompaction: () => compaction };
	}

	const modelCmd = async (s: ReturnType<typeof stub>) =>
		(
			await builtinCommands({
				session: s.session as never,
				registry: s.registry as never,
				onClear: () => {},
				onExit: () => {},
				onModelChanged: (i) => s.changes.push(i as never),
			}).load()
		).find((c) => c.name === "model")!;

	it("moves the tier with the model", async () => {
		const s = stub();
		assert.equal(s.session.tier.name, "medium");
		await (await modelCmd(s)).run({ args: "anthropic/claude-opus-5" });
		assert.equal(s.session.tier.name, "large", "tier did not follow the model");
		assert.equal(s.session.tier.contextWindow, 1_000_000);
	});

	it("re-applies compaction settings for the new window", async () => {
		// pi's defaults overflow a small window; a stale setting from a 1M model
		// would do the same in reverse.
		const s = stub();
		await (await modelCmd(s)).run({ args: "anthropic/claude-opus-5" });
		assert.ok(s.getCompaction(), "compaction was not re-applied");
	});

	it("tells the UI, so the change is visible", async () => {
		const s = stub();
		await (await modelCmd(s)).run({ args: "anthropic/claude-opus-5" });
		assert.equal(s.changes.length, 1);
		assert.equal(s.changes[0].label, "anthropic/claude-opus-5");
	});

	it("does the same when chosen from the picker, not just from an argument", async () => {
		const s = stub();
		const spec = (await (await modelCmd(s)).run({ args: "" })).modal as {
			onSelect: (i: { value: string }) => Promise<unknown>;
		};
		await spec.onSelect({ value: "anthropic/claude-opus-5" });
		assert.equal(s.getModel().id, "claude-opus-5");
		assert.equal(s.session.tier.name, "large", "the picker path skipped the tier update");
	});

	it("rejects a target that is not provider/model", async () => {
		const s = stub();
		const command = await modelCmd(s);
		await assert.rejects(async () => command.run({ args: "nonsense" }));
	});
})
