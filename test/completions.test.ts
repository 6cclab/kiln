import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { builtinCommands } from "../src/commands/builtins.ts";
import { formatTokens } from "../src/tui/transcript.ts";

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
