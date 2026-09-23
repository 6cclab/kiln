import { stripTerminalSequences, type Terminal } from "@earendil-works/pi-tui";

/**
 * A `Terminal` that writes into a string instead of a tty.
 *
 * Everything in `src/tui/` has been unit-tested a component at a time, which
 * leaves one real gap: pi-tui's own render loop is what enforces the width
 * invariant and composes the components into a frame, and none of that runs
 * without a tty. The crash that started this work —
 *
 *     Error: Rendered line 3 exceeds terminal width (172 > 144)
 *
 * was thrown by `doRender`, not by any component. A component test cannot catch
 * its successor.
 *
 * So this implements the 15-method `Terminal` interface against a buffer. The
 * TUI is the real one; only the output device is fake. `start()` captures the
 * input handler, so keystrokes can be delivered exactly as a terminal would
 * deliver them, and `resize()` fires the real resize path.
 */
export class FakeTerminal implements Terminal {
	columns: number;
	rows: number;

	private chunks: string[] = [];
	private onInput?: (data: string) => void;
	private onResize?: () => void;
	private started = false;
	private stopped = false;

	title = "";
	cursorVisible = true;

	constructor(columns = 100, rows = 30) {
		this.columns = columns;
		this.rows = rows;
	}

	start(onInput: (data: string) => void, onResize: () => void): void {
		this.onInput = onInput;
		this.onResize = onResize;
		this.started = true;
	}

	stop(): void {
		this.stopped = true;
	}

	async drainInput(): Promise<void> {}

	write(data: string): void {
		this.chunks.push(data);
	}

	get kittyProtocolActive(): boolean {
		return false;
	}

	moveBy(): void {}
	hideCursor(): void {
		this.cursorVisible = false;
	}
	showCursor(): void {
		this.cursorVisible = true;
	}
	clearLine(): void {}
	clearFromCursor(): void {}
	clearScreen(): void {
		this.chunks = [];
	}
	setTitle(title: string): void {
		this.title = title;
	}
	setProgress(): void {}

	// --- test affordances -------------------------------------------------

	/** Deliver keystrokes the way a terminal would. */
	send(data: string): void {
		if (!this.started) throw new Error("terminal not started");
		this.onInput?.(data);
	}

	resize(columns: number, rows = this.rows): void {
		this.columns = columns;
		this.rows = rows;
		this.onResize?.();
	}

	/** Everything written, escape sequences intact. */
	raw(): string {
		return this.chunks.join("");
	}

	/** Everything written, with ANSI removed. */
	text(): string {
		// `stripTerminalSequences` leaves private-mode sequences such as the
		// synchronized-update markers (`ESC [ ? 2026 h/l`) that pi-tui wraps each
		// frame in. They are not content, and leaving them in makes every
		// assertion about rendered text depend on pi-tui's framing.
		return stripTerminalSequences(this.chunks.join("")).replace(/\u001b\[[?!]?[0-9;]*[a-zA-Z]/g, "");
	}

	/** Non-empty visible lines, in order. */
	lines(): string[] {
		return this.text()
			.split("\n")
			.map((l) => l.replace(/\s+$/, ""))
			.filter((l) => l.length > 0);
	}

	clearCaptured(): void {
		this.chunks = [];
	}

	get isStopped(): boolean {
		return this.stopped;
	}
}
