import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { visibleWidth } from "@earendil-works/pi-tui";
import { TranscriptView } from "../src/tui/app.ts";
import { renderThinking } from "../src/tui/transcript.ts";

/**
 * Reasoning blocks.
 *
 * Captured from a live run: qwen3.8 emits a `thinking` block before most
 * answers (31 deltas for "what is 17 * 23"). Dropped, the user sees a long
 * pause and then a result with no sign of what happened in between.
 */
describe("renderThinking", () => {
	const text = "one\ntwo\nthree";

	it("collapses to a single line by default", () => {
		const lines = renderThinking({ text, expanded: false });
		assert.equal(lines.length, 1);
		assert.ok(lines[0].includes("3 lines"));
		assert.ok(lines[0].includes("ctrl+r"));
	});

	it("shows every line when expanded", () => {
		const lines = renderThinking({ text, expanded: true });
		// Header plus one line each.
		assert.equal(lines.length, 4);
		for (const part of ["one", "two", "three"]) {
			assert.ok(lines.some((l) => l.includes(part)), `${part} missing`);
		}
	});

	it("reads as present tense while streaming and past tense after", () => {
		assert.ok(renderThinking({ text, active: true }).join("").includes("Thinking"));
		assert.ok(renderThinking({ text, active: false }).join("").includes("Thought"));
	});

	it("omits the expand hint while still streaming", () => {
		// There is nothing stable to expand yet.
		assert.ok(!renderThinking({ text, active: true }).join("").includes("ctrl+r"));
	});

	it("renders nothing for an empty block", () => {
		assert.deepEqual(renderThinking({ text: "" }), []);
		assert.deepEqual(renderThinking({ text: "   \n  " }), []);
	});
});

describe("thinking in the transcript", () => {
	const long = Array.from({ length: 40 }, (_, i) => `reasoning step number ${i} with quite a lot of text on it`).join("\n");

	it("does not bury the answer under the reasoning", () => {
		// The motivating case: reasoning models produce more reasoning than
		// answer, every turn.
		const view = new TranscriptView();
		view.appendThinking({ text: long, active: false });
		assert.equal(view.render(80).length, 1);
	});

	it("expands with Ctrl+R alongside tool output", () => {
		const view = new TranscriptView();
		view.appendThinking({ text: long, active: false });
		assert.ok(view.hasCollapsed(), "Ctrl+R would have been a no-op");
		view.toggleExpanded();
		assert.ok(view.render(80).length > 40);
	});

	it("holds the terminal width when expanded", () => {
		const view = new TranscriptView();
		view.appendThinking({ text: "x".repeat(300), active: false });
		view.toggleExpanded();
		const max = Math.max(...view.render(40).map(visibleWidth));
		assert.ok(max <= 40, `overflowed at ${max}`);
	});

	it("streams into one block rather than one per delta", () => {
		// Appending per delta would push a transcript line every few tokens.
		const view = new TranscriptView();
		const streaming = { text: "", active: true };
		view.appendThinking(streaming);
		for (const chunk of ["a", "b", "c"]) streaming.text += chunk;
		assert.equal(view.render(80).length, 1);
		view.toggleExpanded();
		assert.ok(view.render(80).join("").includes("abc"));
	});
});
