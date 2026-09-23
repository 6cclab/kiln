import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
import type { Terminal as XTerm } from "@xterm/headless";

const require = createRequire(import.meta.url);
const { Terminal } = require("@xterm/headless") as typeof import("@xterm/headless");

/**
 * Replay a `HARNESS_RECORD_TTY` recording through a real VT emulator.
 *
 *     npm run replay -- /tmp/harness.rec              # final screen
 *     npm run replay -- /tmp/harness.rec --frames     # screen after every input
 *
 * The point is to answer "what was on the glass", which is the one question a
 * byte log cannot answer and the only one that matters for a layout bug. Blank
 * rows are printed rather than trimmed, because the blank rows ARE the bug.
 */

const [file, ...flags] = process.argv.slice(2);
if (!file) {
	console.error("usage: replay <recording> [--frames] [--cols N] [--rows N]");
	process.exit(1);
}

const flag = (name: string, fallback: number): number => {
	const at = flags.indexOf(name);
	return at >= 0 ? Number(flags[at + 1]) : fallback;
};

const cols = flag("--cols", 100);
const rows = flag("--rows", 40);
const perFrame = flags.includes("--frames");

const vt: XTerm = new Terminal({ cols, rows, allowProposedApi: true, scrollback: 5000 });

const write = async (chunk: string): Promise<void> =>
	new Promise((resolve) => vt.write(chunk, () => resolve()));

/** Visible viewport, blanks preserved so dead space is visible. */
const viewport = (): string[] => {
	const buf = vt.buffer.active;
	const out: string[] = [];
	for (let y = 0; y < vt.rows; y++) {
		out.push(buf.getLine(buf.viewportY + y)?.translateToString(true).replace(/\s+$/, "") ?? "");
	}
	return out;
};

const show = (label: string): void => {
	const view = viewport();
	let lastContent = -1;
	for (let i = 0; i < view.length; i++) if (view[i] !== "") lastContent = i;
	const blanksBelow = view.length - 1 - lastContent;
	console.log(`\n=== ${label} ===`);
	console.log(
		`cursor row ${vt.buffer.active.cursorY} · last content row ${lastContent} · ${blanksBelow} blank rows below`,
	);
	view.forEach((line, i) => console.log(`${String(i).padStart(3)} |${line}`));
};

const raw = readFileSync(file, "utf8");

// Markers are written by the recorder on their own lines and are not terminal
// output; splitting on them recovers the keystroke boundaries.
const parts = raw.split(/(\x00(?:IN|RESIZE):[^\n]*\n)/);

let frame = 0;
for (const part of parts) {
	if (part.startsWith("\x00IN:")) {
		if (perFrame) show(`frame ${frame++} — after input ${part.slice(4).trim()}`);
		continue;
	}
	if (part.startsWith("\x00RESIZE:")) {
		const size = JSON.parse(part.slice(8)) as { columns: number; rows: number };
		vt.resize(size.columns, size.rows);
		continue;
	}
	if (part) await write(part);
}

show("final screen");
