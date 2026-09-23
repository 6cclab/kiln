import { createRequire } from "node:module";
import type { Terminal as XTerm } from "@xterm/headless";
import { FakeTerminal } from "./fake-terminal.ts";

// CommonJS, no named ESM export.
const require = createRequire(import.meta.url);
const { Terminal } = require("@xterm/headless") as typeof import("@xterm/headless");

/**
 * What the user is actually looking at.
 *
 * `FakeTerminal` records the bytes pi-tui writes. That is not a screen, and the
 * difference is where every rendering bug in this project has lived. A closing
 * panel that emits `ESC[2K` for each of its rows looks *correct* in a write-log
 * — the rows are blanked — and still leaves a band of dead space on a real
 * terminal, because blanking a row is not the same as giving it back. A
 * write-log cannot tell the two apart: it has no cursor, no scroll region and
 * no cell grid.
 *
 * `@xterm/headless` is the emulator that backs xterm.js, so it has all three.
 * Pipe `FakeTerminal`'s output into it and the cell grid is the answer.
 *
 * This deliberately does NOT use a pty. It runs the real `TuiMainScreen` and
 * the real components in-process, so tests stay fast and debuggable, and the
 * only thing simulated is the glass. (node-pty would allow driving the shipped
 * binary end to end, but it does not build in this environment — see
 * docs/testing.md.)
 */
export class Screen {
	readonly term: FakeTerminal;
	private vt: XTerm;
	private consumed = 0;

	constructor(cols = 100, rows = 40) {
		this.term = new FakeTerminal(cols, rows);
		this.vt = new Terminal({ cols, rows, allowProposedApi: true, scrollback: 2000 });
	}

	/** Feed anything pi-tui has written since the last call into the emulator. */
	sync(): void {
		const raw = this.term.raw();
		if (raw.length > this.consumed) {
			this.vt.write(raw.slice(this.consumed));
			this.consumed = raw.length;
		}
	}

	/** Let the render loop run, then pick up whatever it wrote. */
	async settle(ms = 80): Promise<void> {
		await new Promise((r) => setTimeout(r, ms));
		this.sync();
		// xterm's write is async; give it a tick to apply.
		await new Promise((r) => setTimeout(r, 10));
	}

	/** The visible viewport, exactly as many rows as the terminal has. */
	viewport(): string[] {
		this.sync();
		const buf = this.vt.buffer.active;
		const out: string[] = [];
		for (let y = 0; y < this.vt.rows; y++) {
			out.push(buf.getLine(buf.viewportY + y)?.translateToString(true).replace(/\s+$/, "") ?? "");
		}
		return out;
	}

	/** Visible rows with trailing blanks trimmed. */
	rows(): string[] {
		const out = this.viewport();
		while (out.length > 0 && out[out.length - 1] === "") out.pop();
		return out;
	}

	text(): string {
		return this.rows().join("\n");
	}

	/**
	 * The row the cursor is parked on, 0-based within the viewport.
	 *
	 * This is the measurement that catches leftover space. After a panel closes,
	 * the content may be three rows tall while the cursor still sits thirty rows
	 * down — and that gap is the blank band the user sees, because the next
	 * thing drawn starts from the cursor, not from the content.
	 */
	cursorRow(): number {
		this.sync();
		return this.vt.buffer.active.cursorY;
	}

	dispose(): void {
		this.vt.dispose();
	}
}
