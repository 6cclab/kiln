import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { summarize } from "../src/tui/app.ts";

/**
 * Rendering a tool result.
 *
 * The bug this exists to prevent: `content` is an ARRAY of blocks, not a
 * string, so a `typeof === "string"` check falls through to `JSON.stringify`
 * and the transcript shows the envelope instead of the output:
 *
 *   {"content":[{"type":"text","text":"line one\nline two\n..."}]}
 *
 * One escaped line, unreadable, and longer than the output it replaced. It
 * looked like a tool misbehaving rather than a rendering fault, which is what
 * made it hard to spot.
 *
 * The shape below was captured from a live `tool_end` event, not assumed.
 */
describe("summarize", () => {
	it("reads the block array a real tool result carries", () => {
		const captured = { content: [{ type: "text", text: "one\ntwo\nthree\n" }] };
		assert.deepEqual(summarize(captured), ["one", "two", "three"]);
	});

	it("never leaks the JSON envelope for a well-formed result", () => {
		const rendered = summarize({ content: [{ type: "text", text: "hello" }] }).join("\n");
		assert.ok(!rendered.includes('"type"'), `envelope leaked: ${rendered}`);
		assert.ok(!rendered.includes("content"), `envelope leaked: ${rendered}`);
	});

	it("drops the empty line a trailing newline produces", () => {
		// Command output almost always ends in a newline; kept, it renders as a
		// blank row under every single tool call.
		assert.deepEqual(summarize({ content: [{ type: "text", text: "x\n" }] }), ["x"]);
	});

	it("keeps interior blank lines, which are real content", () => {
		assert.deepEqual(summarize({ content: [{ type: "text", text: "a\n\nb\n" }] }), ["a", "", "b"]);
	});

	it("handles the plain-string shapes too", () => {
		assert.deepEqual(summarize("a\nb"), ["a", "b"]);
		assert.deepEqual(summarize({ output: "x\ny\n" }), ["x", "y"]);
		assert.deepEqual(summarize({ text: "p" }), ["p"]);
		assert.deepEqual(summarize({ content: "q\n" }), ["q"]);
	});

	it("joins multiple text blocks", () => {
		assert.deepEqual(summarize({ content: [{ type: "text", text: "a" }, { type: "text", text: "b" }] }), ["a", "b"]);
	});

	it("skips blocks it cannot render inline rather than stringifying them", () => {
		// An image block rendered as JSON is noise; the transcript has no way to
		// show it inline anyway.
		assert.deepEqual(summarize({ content: [{ type: "image", data: "base64..." }] }), []);
		assert.deepEqual(summarize({ content: [{ type: "image", data: "x" }, { type: "text", text: "ok" }] }), ["ok"]);
	});

	it("falls back to JSON only for a shape it genuinely cannot read", () => {
		// Still the last resort - just no longer the common path.
		assert.deepEqual(summarize({ weird: 1 }), ['{"weird":1}']);
	});

	it("returns nothing for an empty result", () => {
		assert.deepEqual(summarize(null), []);
		assert.deepEqual(summarize(undefined), []);
		assert.deepEqual(summarize({ content: [] }), []);
	});
});
