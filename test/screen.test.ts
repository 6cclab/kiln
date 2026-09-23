import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { Editor, TuiMainScreen, type Component } from "@earendil-works/pi-tui";
import { Screen } from "./support/screen.ts";
import { BorderedEditor, FooterView, TranscriptView } from "../src/tui/app.ts";
import { createModalHost } from "../src/tui/modal.ts";

/**
 * Assertions about the screen, not about the bytes.
 *
 * `test/tui-render.test.ts` drives the real TUI but asserts on what pi-tui
 * *wrote*. That caught width violations and missed everything about layout over
 * time, because a write-log has no cursor and no cell grid: a panel that blanks
 * its rows with `ESC[2K` and a panel that gives them back produce different
 * screens and equally plausible byte streams.
 *
 * These run the same TUI through a real VT emulator and ask what is on the
 * glass. The invariant is the one a user notices: after a panel closes, the
 * screen is the session again — no band of dead rows, and the cursor back on
 * the content.
 */

const theme = { borderColor: (x: string) => x, selectList: {} };

class Fixed implements Component {
	lines: string[];
	constructor(lines: string[]) {
		this.lines = lines;
	}
	render(): string[] {
		return this.lines;
	}
	invalidate(): void {}
}

interface Session {
	screen: Screen;
	tui: TuiMainScreen;
	modals: ReturnType<typeof createModalHost>;
}

/** A session shaped like the real one: transcript, input box, footer. */
function session(transcriptRows: number, cols = 100, rows = 40): Session {
	const screen = new Screen(cols, rows);
	const tui = new TuiMainScreen(screen.term);
	tui.setClearOnShrink(true);

	const transcript = new TranscriptView();
	for (let i = 0; i < transcriptRows; i++) transcript.append([`transcript row ${i}`]);
	const box = new BorderedEditor(new Editor(tui, theme as never, { paddingX: 1 }), (x: string) => x);

	tui.addChild(transcript);
	tui.addChild(box);
	tui.addChild(
		new FooterView({ modelLabel: "test/model", contextWindow: 49_152, mode: "auto", startedAt: Date.now() }),
	);
	tui.setFocus(box);
	tui.start();

	return { screen, tui, modals: createModalHost(tui) };
}

describe("screen after a panel closes", () => {
	// Both sides of the scroll boundary: a short session leaves the frame well
	// inside the viewport, a long one has already filled it. The rendering path
	// differs between them, and only one of them was ever tested.
	for (const transcriptRows of [5, 60]) {
		it(`leaves no dead rows (transcript ${transcriptRows} rows)`, async () => {
			const { screen, tui, modals } = session(transcriptRows);
			await screen.settle(150);
			const before = screen.rows().length;

			await modals.open({
				title: "Model",
				items: () => Array.from({ length: 20 }, (_, i) => ({ value: `m${i}`, label: `MODEL-${i}` })),
				onSelect: () => ({ close: true as const }),
			});
			await screen.settle(150);
			assert.match(screen.text(), /MODEL-0/, "panel should be on screen");

			screen.term.send("\r");
			await screen.settle(250);

			const after = screen.rows();
			assert.equal(
				after.filter((r) => r.includes("MODEL-")).length,
				0,
				`panel rows still on screen:\n${after.join("\n")}`,
			);
			assert.equal(after.length, before, `frame height changed: ${before} -> ${after.length}`);
			// The cursor is what the next frame draws from, so a cursor parked
			// below the content is dead space even when every row is blank.
			assert.ok(
				screen.cursorRow() <= after.length,
				`cursor at row ${screen.cursorRow()} but content ends at ${after.length}`,
			);

			tui.stop();
			screen.dispose();
		});
	}

	it("gives the rows back when a tall frame shrinks", async () => {
		const screen = new Screen(100, 40);
		const tui = new TuiMainScreen(screen.term);
		tui.setClearOnShrink(true);
		const body = new Fixed(Array.from({ length: 25 }, (_, i) => `row-${i}`));
		tui.addChild(body);
		tui.start();
		await screen.settle(120);
		assert.equal(screen.rows().length, 25);

		body.lines = ["row-0", "row-1"];
		tui.requestRender();
		await screen.settle(200);

		assert.deepEqual(screen.rows(), ["row-0", "row-1"]);
		assert.ok(screen.cursorRow() <= 2, `cursor left at row ${screen.cursorRow()}`);

		tui.stop();
		screen.dispose();
	});
});
