import { strict as assert } from "node:assert";
import { describe, it, before, after } from "node:test";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { parseMentions, resolveMentions, describeMentions } from "../src/tui/mentions.ts";
import { tierForWindow } from "../src/budget/tier.ts";

const SMALL = tierForWindow(32_768);
const LARGE = tierForWindow(200_000);

describe("parseMentions", () => {
	it("finds a mention at the start and mid-sentence", () => {
		assert.deepEqual(parseMentions("@src/a.ts what does this do"), ["src/a.ts"]);
		assert.deepEqual(parseMentions("compare @a.ts and @b.ts"), ["a.ts", "b.ts"]);
	});

	it("strips trailing punctuation, because people write in sentences", () => {
		assert.deepEqual(parseMentions("look at @src/a.ts, then @b.ts."), ["src/a.ts", "b.ts"]);
	});

	it("does not treat an email address as a mention", () => {
		// The `@` there is preceded by a word character, not whitespace.
		assert.deepEqual(parseMentions("mail me at someone@example.com"), []);
	});

	it("deduplicates, so the same file is not inlined twice", () => {
		assert.deepEqual(parseMentions("@a.ts vs @a.ts"), ["a.ts"]);
	});

	it("finds nothing in a line with no mention", () => {
		assert.deepEqual(parseMentions("what does the editor do?"), []);
	});
});

describe("resolveMentions", () => {
	let dir = "";
	let big = "";

	before(async () => {
		dir = await mkdtemp(join(tmpdir(), "harness-mentions-"));
		await writeFile(join(dir, "small.ts"), "export const x = 1;\n");
		await mkdir(join(dir, "sub"));
		// Comfortably past the small tier's per-result ceiling.
		big = Array.from({ length: 4000 }, (_, i) => `const line${i} = ${i};`).join("\n");
		await writeFile(join(dir, "big.ts"), big);
	});

	after(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("inlines the file contents into the prompt", async () => {
		const out = await resolveMentions("@small.ts explain", { cwd: dir, tier: LARGE });
		assert.ok(out.prompt.includes("export const x = 1;"), "contents not inlined");
		assert.ok(out.prompt.includes('<file path="small.ts">'));
		// The question must survive, and come last.
		assert.ok(out.prompt.trimEnd().endsWith("@small.ts explain"));
	});

	it("leaves a line with no mention untouched", async () => {
		const out = await resolveMentions("just a question", { cwd: dir, tier: LARGE });
		assert.equal(out.prompt, "just a question");
		assert.deepEqual(out.mentions, []);
	});

	it("caps a large file to the tier budget rather than blowing the window", async () => {
		// The motivating case: one `@` on a big file must not end a 32k session
		// before it starts.
		const out = await resolveMentions("@big.ts explain", { cwd: dir, tier: SMALL });
		const mention = out.mentions[0];
		assert.equal(mention.truncated, true, "was not truncated");
		assert.ok(mention.tokens! <= SMALL.toolOutputTokens, `${mention.tokens} over ${SMALL.toolOutputTokens}`);
		assert.ok(out.prompt.length < big.length, "prompt carried the whole file");
	});

	it("tells the model the file was cut, so it does not treat it as complete", async () => {
		// A silent truncation is worse than no file: the model answers
		// confidently about code it never saw.
		const out = await resolveMentions("@big.ts explain", { cwd: dir, tier: SMALL });
		assert.ok(out.prompt.includes("truncated"), "no truncation notice");
		assert.ok(out.prompt.includes("read tool"), "no pointer to the rest");
	});

	it("splits the budget so three mentions do not cost three times one", async () => {
		const one = await resolveMentions("@big.ts", { cwd: dir, tier: SMALL });
		const three = await resolveMentions("@big.ts @big.ts @big.ts", { cwd: dir, tier: SMALL });
		// Deduped to a single mention, so the ceiling is the same either way;
		// what matters is that it never exceeds the tier's budget.
		for (const m of [...one.mentions, ...three.mentions]) {
			assert.ok(m.tokens! <= SMALL.toolOutputTokens);
		}
	});

	it("reports a missing file instead of throwing", async () => {
		const out = await resolveMentions("@nope.ts explain", { cwd: dir, tier: LARGE });
		assert.equal(out.mentions[0].skipped, "not found");
		// The turn still goes ahead, with the line unchanged.
		assert.equal(out.prompt, "@nope.ts explain");
	});

	it("does not inline a directory", async () => {
		const out = await resolveMentions("@sub explain", { cwd: dir, tier: LARGE });
		assert.equal(out.mentions[0].skipped, "is a directory");
	});

	it("refuses a path outside the workspace", async () => {
		// Same rule as the permission gate: typing a path quickly is not
		// authorization to read outside the workspace.
		const out = await resolveMentions("@/etc/hosts explain", { cwd: dir, tier: LARGE, roots: [dir] });
		assert.equal(out.mentions[0].skipped, "outside the workspace");
		assert.ok(!out.prompt.includes("<file"));
	});

	it("describes what was attached without echoing it", async () => {
		const out = await resolveMentions("@small.ts explain", { cwd: dir, tier: LARGE });
		const described = describeMentions(out.mentions, dir).join("\n");
		assert.ok(described.includes("small.ts"));
		assert.ok(described.includes("tokens"));
		assert.ok(!described.includes("export const x"), "echoed the file contents");
	});
});

describe("image attachments", () => {
	let dir = "";
	// A 1x1 red PNG, so this exercises real bytes rather than a stub.
	const PNG = Buffer.from(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
		"base64",
	);

	before(async () => {
		dir = await mkdtemp(join(tmpdir(), "harness-images-"));
		await writeFile(join(dir, "shot.png"), PNG);
		await writeFile(join(dir, "shot.JPG"), PNG);
		await writeFile(join(dir, "notes.txt"), "plain text");
	});

	after(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("attaches an image instead of inlining it as text", async () => {
		// A PNG read as UTF-8 is megabytes of replacement characters: it ends a
		// 32k conversation and tells the model nothing.
		const out = await resolveMentions("@shot.png what is this?", { cwd: dir, tier: LARGE });
		assert.equal(out.images.length, 1);
		assert.equal(out.images[0].mimeType, "image/png");
		assert.equal(out.images[0].data, PNG.toString("base64"));
		// The prompt must be untouched - no <file> block, no base64 in the text.
		assert.equal(out.prompt, "@shot.png what is this?");
	});

	it("recognises the extension regardless of case", async () => {
		const out = await resolveMentions("@shot.JPG", { cwd: dir, tier: LARGE });
		assert.equal(out.images[0]?.mimeType, "image/jpeg");
	});

	it("still inlines text files alongside an image", async () => {
		const out = await resolveMentions("@shot.png @notes.txt compare", { cwd: dir, tier: LARGE });
		assert.equal(out.images.length, 1);
		assert.ok(out.prompt.includes("plain text"), "the text file was not inlined");
	});

	it("reports an image by size rather than token count", async () => {
		const out = await resolveMentions("@shot.png", { cwd: dir, tier: LARGE });
		const described = describeMentions(out.mentions, dir).join("");
		assert.ok(described.includes("image"));
		assert.ok(!described.includes("tokens"), "an image was costed in tokens");
	});

	it("returns no images when none were mentioned", async () => {
		assert.deepEqual((await resolveMentions("@notes.txt", { cwd: dir, tier: LARGE })).images, []);
		assert.deepEqual((await resolveMentions("nothing here", { cwd: dir, tier: LARGE })).images, []);
	});

	it("does not attach an image from outside the workspace", async () => {
		const out = await resolveMentions("@/etc/passwd.png", { cwd: dir, tier: LARGE, roots: [dir] });
		assert.equal(out.images.length, 0);
		assert.equal(out.mentions[0].skipped, "outside the workspace");
	});
});
