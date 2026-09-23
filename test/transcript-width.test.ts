import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { visibleWidth } from "@earendil-works/pi-tui";
import { FooterView, SpinnerView, TranscriptView } from "../src/tui/app.ts";
import { PermissionPromptView } from "../src/tui/permission-prompt.ts";

/**
 * pi-tui throws when a component returns a line wider than the viewport --
 * an overflowing line corrupts its differential rendering, so it refuses
 * rather than draws garbage. The transcript is the component most exposed to
 * content it did not author: a pasted URL, a long path in a tool call, an
 * error message from somewhere else.
 *
 * Observed as a crash on a pasted URL:
 *   Error: Rendered line 3 exceeds terminal width (172 > 144).
 *
 * Every block kind is exercised because each takes a different path to a line.
 */

const WIDTH = 40;
// No spaces past the scheme: a word-wrapper has nothing to break on, so this
// also covers the hard-break case rather than only the easy one.
const LONG = `https://example.com/${"a".repeat(180)}`;

function widest(view: TranscriptView): number {
	const lines = view.render(WIDTH);
	return lines.length === 0 ? 0 : Math.max(...lines.map(visibleWidth));
}

describe("TranscriptView width", () => {
	it("wraps a plain line that exceeds the width", () => {
		const view = new TranscriptView();
		view.append([`> ${LONG}`]);
		assert.ok(widest(view) <= WIDTH, `overflowed at ${widest(view)}`);
	});

	it("keeps every character when wrapping", () => {
		// Wrapping rather than truncating is the point: a transcript that
		// silently drops the end of a pasted URL is worse than one that takes
		// an extra row.
		const view = new TranscriptView();
		view.append([LONG]);
		assert.equal(view.render(WIDTH).join(""), LONG);
	});

	it("emits more than one line for content that does not fit", () => {
		// Guards against a "fix" that satisfies the width check by truncating.
		const view = new TranscriptView();
		view.append([LONG]);
		assert.ok(view.render(WIDTH).length >= Math.ceil(LONG.length / WIDTH));
	});

	for (const [kind, markdown] of [
		["bare url", LONG],
		["inline code", `\`${LONG}\``],
		["fenced code", `\`\`\`\n${LONG}\n\`\`\``],
		["table", `| a | b |\n|---|---|\n| ${LONG} | x |`],
		["prose", "word ".repeat(80)],
	] as const) {
		it(`holds the width for markdown: ${kind}`, () => {
			const view = new TranscriptView();
			view.appendMarkdown(markdown);
			assert.ok(widest(view) <= WIDTH, `overflowed at ${widest(view)}`);
		});
	}

	it("holds the width for a tool call, collapsed and expanded", () => {
		const view = new TranscriptView();
		view.appendToolCall({ name: "read", primaryArg: LONG, status: "ok", resultLines: [LONG], totalLines: 2 }, [
			LONG,
			LONG,
		]);
		assert.ok(widest(view) <= WIDTH, `collapsed overflowed at ${widest(view)}`);
		view.toggleExpanded();
		assert.ok(widest(view) <= WIDTH, `expanded overflowed at ${widest(view)}`);
	});

	it("does not wrap lines that already fit", () => {
		// The common case must stay a passthrough; wrapping a short line would
		// reflow the whole transcript for nothing.
		const view = new TranscriptView();
		view.append(["short", "also short"]);
		assert.deepEqual(view.render(WIDTH), ["short", "also short"]);
	});
});

/**
 * The transcript is where the crash was observed, but every component pi-tui
 * renders is held to the same rule, and the rest show content from elsewhere
 * too: a tool label, a diff hunk, a model-written plan.
 */
describe("other components hold the width", () => {
	it("truncates the spinner to one row", () => {
		const spinner = new SpinnerView();
		spinner.start(0);
		spinner.setTokens(123_456);
		const lines = spinner.render(WIDTH);
		// One row, not two: a wrapping spinner makes the transcript above it
		// jump on every tick.
		assert.equal(lines.length, 1);
		assert.ok(visibleWidth(lines[0]) <= WIDTH, `overflowed at ${visibleWidth(lines[0])}`);
	});

	it("truncates the status line to two rows", () => {
		// Two rows by definition: status, then mode. A third would push the input
		// box around as the numbers change.
		const footer = new FooterView({
			modelLabel: "ollama/qwen3.8",
			contextWindow: 49_152,
			mode: "auto",
			startedAt: 0,
			now: 0,
		});
		footer.update({ modelLabel: LONG, contextUsed: 20_000, git: { branch: LONG, dirty: true } });
		const lines = footer.render(WIDTH);
		assert.equal(lines.length, 2);
		for (const line of lines) assert.ok(visibleWidth(line) <= WIDTH, `overflowed at ${visibleWidth(line)}`);
	});

	it("wraps a permission prompt for a long command", () => {
		const view = new PermissionPromptView();
		// Not awaited: the prompt renders while the promise is pending, which is
		// the state being tested.
		void view.ask({ toolName: "bash", primaryArg: `echo ${LONG}`, args: { command: `echo ${LONG}` } });
		const max = Math.max(...view.render(WIDTH).map(visibleWidth));
		assert.ok(max <= WIDTH, `overflowed at ${max}`);
	});

	it("wraps a plan with lines longer than the terminal", () => {
		// Plans are model-written prose. Long lines are the norm, so this path
		// would crash on approval rather than in some edge case.
		const view = new PermissionPromptView();
		void view.askPlan(`# Plan\n\n1. ${"do a thing and then another thing ".repeat(8)}\n2. ${LONG}`);
		const max = Math.max(...view.render(WIDTH).map(visibleWidth));
		assert.ok(max <= WIDTH, `overflowed at ${max}`);
	});
});

describe("user message", () => {
	it("marks the first line with a pointer and no fill", () => {
		// Claude Code's user message is a subtle `❯` then the text — no full-width
		// band. The pointer marks the line; the assistant's prose shares the left
		// margin, so the two voices differ without filling rows.
		const view = new TranscriptView();
		view.appendUser("short question");
		assert.deepEqual(view.render(WIDTH), ["❯ short question"]);
	});

	it("marks only the first wrapped line", () => {
		const view = new TranscriptView();
		view.appendUser(`short ${"very long segment ".repeat(6)}tail`);
		const lines = view.render(WIDTH);
		assert.equal(lines.filter((l) => l.includes("❯")).length, 1, "pointer repeated past the first line");
		for (const line of lines) assert.ok(visibleWidth(line) <= WIDTH, `overflowed at ${visibleWidth(line)}`);
	});

	it("wraps a long message instead of overflowing", () => {
		const view = new TranscriptView();
		view.appendUser(LONG);
		const lines = view.render(WIDTH);
		assert.ok(lines.length > 1);
		for (const line of lines) assert.ok(visibleWidth(line) <= WIDTH, `overflowed at ${visibleWidth(line)}`);
	});

	it("keeps the text findable, which is the whole point", () => {
		const view = new TranscriptView();
		view.appendUser("find me later");
		assert.ok(view.render(WIDTH).join("").includes("find me later"));
	});

	it("re-wraps when the terminal resizes", () => {
		// Held as text rather than pre-wrapped lines for exactly this.
		const view = new TranscriptView();
		view.appendUser("a ".repeat(60));
		assert.notEqual(view.render(40).length, view.render(100).length);
	});
});

describe("turn summary", () => {
	it("leads with elapsed time, the number being budgeted against", async () => {
		const { renderTurnSummary } = await import("../src/tui/transcript.ts");
		const line = renderTurnSummary({ seconds: 32, tokens: 4200, toolCalls: 1 }).join("");
		assert.ok(line.includes("32s"));
		assert.ok(line.indexOf("32s") < line.indexOf("4.2k"), "tokens led instead of time");
	});

	it("omits parts it has nothing to say about", async () => {
		const { renderTurnSummary } = await import("../src/tui/transcript.ts");
		const line = renderTurnSummary({ seconds: 5 }).join("");
		assert.ok(!line.includes("tool call"));
		assert.ok(!line.includes("tokens"));
	});

	it("gets the plural right", async () => {
		const { renderTurnSummary } = await import("../src/tui/transcript.ts");
		assert.ok(renderTurnSummary({ seconds: 1, toolCalls: 1 }).join("").includes("1 tool call ·") === false);
		assert.ok(renderTurnSummary({ seconds: 1, toolCalls: 1 }).join("").includes("1 tool call"));
		assert.ok(renderTurnSummary({ seconds: 1, toolCalls: 3 }).join("").includes("3 tool calls"));
	});
});
