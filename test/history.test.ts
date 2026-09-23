import { strict as assert } from "node:assert";
import { describe, it, before, after } from "node:test";
import { mkdtemp, rm, readFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { appendHistory, loadHistory, historyPath } from "../src/tui/history.ts";

/**
 * Prompt history.
 *
 * pi-tui's editor navigates history with the arrow keys, but only over what it
 * has been given — and nothing called `addToHistory`, so up-arrow walked an
 * empty list and read as a dead key.
 */
describe("history", () => {
	let dir = "";
	const file = () => join(dir, "history");

	before(async () => {
		dir = await mkdtemp(join(tmpdir(), "harness-history-"));
	});

	after(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("returns nothing on a first run rather than failing", async () => {
		assert.deepEqual(await loadHistory(join(dir, "absent")), []);
	});

	it("round-trips an entry", async () => {
		await appendHistory("fix the build", file());
		assert.deepEqual(await loadHistory(file()), ["fix the build"]);
	});

	it("keeps the most recent last, the direction the editor walks back from", async () => {
		const path = join(dir, "ordered");
		await appendHistory("first", path);
		await appendHistory("second", path);
		assert.deepEqual(await loadHistory(path), ["first", "second"]);
	});

	it("keeps a multi-line prompt as one entry", async () => {
		// Written as one line with escapes; otherwise a pasted block becomes
		// twenty history entries, one per line.
		const path = join(dir, "multiline");
		await appendHistory("line one\nline two", path);
		assert.deepEqual(await loadHistory(path), ["line one\nline two"]);
		assert.equal((await readFile(path, "utf8")).split("\n").filter(Boolean).length, 1);
	});

	it("appends rather than rewriting, so two sessions do not truncate each other", async () => {
		const path = join(dir, "concurrent");
		await Promise.all([appendHistory("a", path), appendHistory("b", path), appendHistory("c", path)]);
		assert.equal((await loadHistory(path)).length, 3);
	});

	it("ignores blank input", async () => {
		const path = join(dir, "blank");
		await appendHistory("   ", path);
		await appendHistory("", path);
		assert.deepEqual(await loadHistory(path), []);
	});

	it("never throws when the file cannot be written", async () => {
		// History is a convenience; failing to record it must not cost the turn.
		await appendHistory("x", "/nonexistent-root/nope/history");
	});

	it("lives outside the project", () => {
		// A prompt can contain anything you typed; a repo directory invites
		// committing it.
		assert.ok(historyPath().includes("/.harness/"), historyPath());
	});
});
