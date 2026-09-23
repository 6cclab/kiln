import { strict as assert } from "node:assert";
import { describe, it, before, after } from "node:test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { loadKeybindings, keybindingsPath } from "../src/claude/keybindings.ts";

describe("loadKeybindings", () => {
	let dir = "";

	before(async () => {
		dir = await mkdtemp(join(tmpdir(), "harness-keys-"));
	});

	after(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("treats an absent file as normal rather than an error", async () => {
		// Most machines have no keybindings.json; defaults simply apply.
		const result = await loadKeybindings(join(dir, "missing.json"));
		assert.equal(result.loaded, false);
		assert.equal(result.error, undefined);
	});

	it("reports a broken file instead of silently using defaults", async () => {
		// Falling back without a word looks identical to the overrides not
		// working, which is a long afternoon.
		const path = join(dir, "broken.json");
		await writeFile(path, "{ not json");
		const result = await loadKeybindings(path);
		assert.equal(result.loaded, false);
		assert.ok(result.error?.includes(path));
	});

	it("rejects a file that parses but is not an object", async () => {
		const path = join(dir, "array.json");
		await writeFile(path, "[1,2,3]");
		assert.ok((await loadKeybindings(path)).error);
	});

	it("applies a valid override file", async () => {
		const path = join(dir, "ok.json");
		await writeFile(path, JSON.stringify({}));
		const result = await loadKeybindings(path);
		assert.equal(result.loaded, true);
		assert.equal(result.error, undefined);
	});

	it("looks in the same place Claude Code does", () => {
		assert.ok(keybindingsPath().endsWith("/.claude/keybindings.json"));
	});
});
