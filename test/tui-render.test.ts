import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { Editor, TuiMainScreen } from "@earendil-works/pi-tui";
import { FakeTerminal } from "./support/fake-terminal.ts";
import { BorderedEditor, FooterView, SpinnerView, TranscriptView } from "../src/tui/app.ts";
import { PermissionPromptView } from "../src/tui/permission-prompt.ts";

/**
 * The TUI driven through pi-tui's own render loop.
 *
 * Every component here has unit tests, and they all passed while the session
 * still crashed:
 *
 *     Error: Rendered line 3 exceeds terminal width (172 > 144)
 *
 * That was thrown by `TuiMainScreen.doRender`, not by any component — the
 * invariant is enforced where the frame is composed, so only a test that
 * composes a frame can catch a violation of it. These tests use the real TUI
 * and fake only the output device.
 */

const settle = () => new Promise((r) => setTimeout(r, 80));

interface Harness {
	editor: BorderedEditor;
	term: FakeTerminal;
	tui: TuiMainScreen;
	transcript: TranscriptView;
	spinner: SpinnerView;
	footer: FooterView;
	permission: PermissionPromptView;
	render: () => Promise<void>;
	stop: () => void;
}

/** The same component stack and order `runApp` builds. */
function harness(columns = 100, rows = 30): Harness {
	const term = new FakeTerminal(columns, rows);
	const tui = new TuiMainScreen(term);

	const transcript = new TranscriptView();
	const spinner = new SpinnerView();
	const permission = new PermissionPromptView();
	const footer = new FooterView({ modelLabel: "ollama/qwen3.8", contextWindow: 49152, mode: "auto", startedAt: 0, now: 0 });
	const theme = {
		borderColor: (t: string) => t,
		selectList: {
			selectedPrefix: (t: string) => t,
			selectedText: (t: string) => t,
			description: (t: string) => t,
			scrollInfo: (t: string) => t,
			noMatch: (t: string) => t,
		},
	};
	// The real BorderedEditor, so the box edges are part of what is asserted on.
	const editor: BorderedEditor = new BorderedEditor(new Editor(tui as never, theme, { paddingX: 1 }), (t) => t);

	// Order is the layout: transcript, spinner, prompt, input, footer.
	tui.addChild(transcript);
	tui.addChild(spinner);
	tui.addChild(permission);
	tui.addChild(editor as never);
	tui.addChild(footer);
	// Without this the editor never receives a keystroke: the TUI routes input
	// to the focused component, and nothing is focused by default.
	tui.setFocus(editor as never);
	tui.start();

	return {
		editor,
		term,
		tui,
		transcript,
		spinner,
		footer,
		permission,
		render: async () => {
			tui.requestRender(true);
			await settle();
		},
		stop: () => tui.stop(),
	};
}

describe("full-frame rendering", () => {
	it("renders a frame without pi-tui rejecting it", async () => {
		const h = harness();
		h.transcript.append(["> hello"]);
		h.footer.update({ contextUsed: 12_000, git: { branch: "main", dirty: false } });
		await h.render();
		assert.ok(h.term.lines().some((l) => l.includes("hello")));
		h.stop();
	});

	it("survives a pasted URL far wider than the terminal", async () => {
		// The original crash, end to end. pi-tui throws from doRender, so this
		// fails loudly if the wrapping in TranscriptView ever regresses.
		const h = harness(80, 24);
		const url = `https://github.com/example/${"segment-".repeat(30)}end`;
		h.transcript.append([`> Look at ${url}`]);
		await h.render();
		for (const line of h.term.lines()) {
			assert.ok(line.length <= 80, `line of ${line.length} chars in an 80-column terminal`);
		}
		h.stop();
	});

	it("survives a permission prompt carrying a long command", async () => {
		const h = harness(80, 24);
		void h.permission.ask({
			toolName: "bash",
			primaryArg: `echo ${"x".repeat(400)}`,
			args: { command: `echo ${"x".repeat(400)}` },
		});
		await h.render();
		for (const line of h.term.lines()) assert.ok(line.length <= 80, `overflow: ${line.length}`);
		h.stop();
	});

	it("survives a plan of model-written prose", async () => {
		// Plans are long lines by nature; this path would have crashed on the
		// first plan ever approved.
		const h = harness(80, 24);
		void h.permission.askPlan(`# Plan\n\n${"1. do a thing and then another thing ".repeat(12)}`);
		await h.render();
		for (const line of h.term.lines()) assert.ok(line.length <= 80, `overflow: ${line.length}`);
		h.stop();
	});

	it("survives every block kind in one frame", async () => {
		const h = harness(72, 30);
		const wide = "y".repeat(300);
		h.transcript.append([`> ${wide}`]);
		h.transcript.appendMarkdown(`| a | b |\n|---|---|\n| ${wide} | x |`);
		h.transcript.appendToolCall(
			{ name: "read", primaryArg: wide, status: "ok", resultLines: [wide], totalLines: 2 },
			[wide, wide],
		);
		h.transcript.appendThinking({ text: wide, active: false });
		h.spinner.start(1);
		h.footer.update({ modelLabel: wide });
		await h.render();
		for (const line of h.term.lines()) assert.ok(line.length <= 72, `overflow: ${line.length}`);
		h.stop();
	});

	it("reflows rather than crashing when the terminal narrows", async () => {
		// A resize re-renders every component at the new width. Content that fit
		// before must not overflow after.
		const h = harness(120, 30);
		h.transcript.appendMarkdown("a ".repeat(200));
		await h.render();

		for (const width of [90, 60, 40, 20]) {
			h.term.clearCaptured();
			h.term.resize(width);
			await h.render();
			for (const line of h.term.lines()) {
				assert.ok(line.length <= width, `overflow at width ${width}: ${line.length}`);
			}
		}
		h.stop();
	});

	it("puts the spinner above the input and the footer below it", async () => {
		// Split into two components precisely because they sit on opposite sides
		// of the editor; a single status component put the footer in the wrong
		// place.
		const h = harness(100, 30);
		h.spinner.start(1);
		h.footer.update({ modelLabel: "FOOTER-MARKER" });
		await h.render();

		const lines = h.term.lines();
		const spinnerAt = lines.findIndex((l) => /esc to interrupt/.test(l));
		const footerAt = lines.findIndex((l) => l.includes("FOOTER-MARKER"));
		// The input area is a single rule now, not a box.
		const boxAt = lines.findIndex((l) => /^─+$/.test(l));

		assert.ok(spinnerAt >= 0, "no spinner rendered");
		assert.ok(footerAt >= 0, "no footer rendered");
		assert.ok(boxAt >= 0, "no input box rendered");
		assert.ok(spinnerAt < boxAt, "spinner rendered below the input box");
		assert.ok(footerAt > boxAt, "footer rendered above the input box");
		h.stop();
	});

	it("shows reasoning collapsed, so the answer stays visible", async () => {
		const h = harness(100, 30);
		h.transcript.appendThinking({ text: Array.from({ length: 30 }, (_, i) => `step ${i}`).join("\n") });
		h.transcript.appendMarkdown("The answer is 391.");
		await h.render();

		const lines = h.term.lines();
		assert.ok(lines.some((l) => l.includes("The answer is 391")), "the answer was not rendered");
		assert.ok(!lines.some((l) => l.includes("step 17")), "reasoning was rendered expanded");
		h.stop();
	});

	it("reveals the reasoning when expanded", async () => {
		const h = harness(100, 40);
		h.transcript.appendThinking({ text: "step one\nstep two\nstep three" });
		h.transcript.toggleExpanded();
		await h.render();
		assert.ok(h.term.lines().some((l) => l.includes("step two")));
		h.stop();
	});

	it("keeps the frame valid while the spinner animates", async () => {
		// The spinner re-renders on a timer; a frame it produces is as much a
		// frame as any other.
		const h = harness(64, 20);
		h.spinner.start(3);
		h.spinner.setTokens(123_456);
		for (let i = 0; i < 5; i++) {
			h.spinner.tick();
			h.term.clearCaptured();
			await h.render();
			for (const line of h.term.lines()) assert.ok(line.length <= 64, `overflow: ${line.length}`);
		}
		h.stop();
	});
});

describe("keyboard input through the real TUI", () => {
	/**
	 * These go through pi-tui's actual input path — `terminal.start()` hands it
	 * a callback, and `send()` invokes that callback exactly as a tty would.
	 * The key router is unit-tested separately; what this adds is proof that the
	 * bytes reach it at all, which is the part `setFocus` silently governs.
	 */

	it("delivers typed characters to the editor", async () => {
		const h = harness();
		h.term.send("hello world");
		await h.render();
		assert.ok(h.term.lines().some((l) => l.includes("hello world")), "typed text never rendered");
		h.stop();
	});

	it("renders a pasted line wider than the terminal without overflowing", async () => {
		// Paste arrives as one large chunk on stdin, not as keystrokes.
		const h = harness(60, 20);
		h.term.send(`https://example.com/${"x".repeat(300)}`);
		await h.render();
		for (const line of h.term.lines()) assert.ok(line.length <= 60, `overflow: ${line.length}`);
		h.stop();
	});

	it("routes a control byte to the listener rather than inserting it", async () => {
		const h = harness();
		let sawCtrlR = false;
		h.tui.addInputListener((data) => {
			if (data === "\x12") {
				sawCtrlR = true;
				return { consume: true };
			}
			return undefined;
		});
		h.term.send("\x12");
		await h.render();
		assert.ok(sawCtrlR, "Ctrl+R never reached an input listener");
		// And it must not have been typed into the box as a literal character.
		assert.ok(!h.term.text().includes("\x12"));
		h.stop();
	});

	it("keeps the frame valid while text is typed at a narrow width", async () => {
		const h = harness(30, 12);
		for (const chunk of ["abc ", "def ", "a much longer stretch of text ".repeat(3)]) {
			h.term.send(chunk);
			h.term.clearCaptured();
			await h.render();
			for (const line of h.term.lines()) assert.ok(line.length <= 30, `overflow: ${line.length}`);
		}
		h.stop();
	});
});

describe("consumed keys still redraw", () => {
	/**
	 * pi-tui stops dispatching as soon as a listener returns `{consume: true}`:
	 *
	 *     const result = listener(current);
	 *     if (result?.consume) { return; }
	 *
	 * So a design that consumes keys in one listener and re-renders in a second
	 * never redraws for exactly the keys that changed something. Every consumed
	 * key mutated state and left the screen untouched, which is
	 * indistinguishable from the key being dead — reported as "esc does nothing,
	 * ctrl c does nothing".
	 */

	it("stops dispatch at the first consuming listener", async () => {
		// The upstream behaviour this depends on. If pi-tui ever changes it, this
		// fails and says why rather than leaving the reason to be rediscovered.
		const h = harness();
		const reached: string[] = [];
		h.tui.addInputListener(() => {
			reached.push("first");
			return { consume: true };
		});
		h.tui.addInputListener(() => {
			reached.push("second");
			return undefined;
		});
		h.term.send("x");
		await h.render();
		assert.deepEqual(reached, ["first"], "dispatch continued past a consuming listener");
		h.stop();
	});

	it("redraws when a consuming listener changes what is on screen", async () => {
		const h = harness();
		// Let the first frame settle before measuring a diff against it: the
		// differential renderer has nothing to diff until one frame exists.
		await h.render();

		// The shape runApp uses: consume and request the render in one place.
		h.tui.addInputListener((data) => {
			if (data !== "\x0c") return undefined;
			h.transcript.append(["CLEARED-AND-REDRAWN"]);
			h.tui.requestRender();
			return { consume: true };
		});

		h.term.clearCaptured();
		h.term.send("\x0c");
		await new Promise((r) => setTimeout(r, 80));

		assert.ok(
			h.term.lines().some((l) => l.includes("CLEARED-AND-REDRAWN")),
			"a consumed key changed state without redrawing",
		);
		h.stop();
	});
})
