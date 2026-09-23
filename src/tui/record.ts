import { appendFileSync, mkdirSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";
import type { Terminal } from "@earendil-works/pi-tui";

/**
 * A `Terminal` that tees everything to a file on its way to the tty.
 *
 * Rendering bugs in this project have all had the same shape: they are invisible
 * in a component test, invisible in a byte-level assertion, and obvious the
 * moment a human looks at the screen. The missing piece was never analysis — it
 * was a recording. Guessing at a repro from a screenshot has now been wrong
 * twice.
 *
 * With `HARNESS_RECORD_TTY=/path/to/file` set, the exact byte stream the
 * terminal received is on disk. `npm run replay -- <file>` feeds it through a
 * real VT emulator and prints the screen, so a bug that happens once on a real
 * terminal can be reproduced offline, forever, without the model it was talking
 * to or the terminal it happened on.
 *
 * Input is recorded too, as `\x00IN:<json>\n` marker lines. The keystroke that
 * triggered a frame is usually the whole question, and a stream of output with
 * no idea which key produced it is much harder to read.
 */
export function recordingTerminal(inner: Terminal, path: string): Terminal {
	mkdirSync(dirname(path), { recursive: true });
	writeFileSync(path, "");

	const append = (text: string): void => {
		try {
			appendFileSync(path, text);
		} catch {
			// A recording that fails must never take the session with it.
		}
	};

	// Delegated explicitly rather than by Proxy: `Terminal` is a small, stable
	// interface, and a Proxy here would silently forward a method that later
	// needs recording too.
	return new Proxy(inner, {
		get(target, prop, receiver) {
			if (prop === "write") {
				return (data: string) => {
					append(data);
					return (target as Terminal).write(data);
				};
			}
			if (prop === "start") {
				return (onInput: (d: string) => void, onResize: () => void) =>
					(target as Terminal).start((data) => {
						append(`\x00IN:${JSON.stringify(data)}\n`);
						onInput(data);
					}, () => {
						append(`\x00RESIZE:${JSON.stringify({ columns: target.columns, rows: target.rows })}\n`);
						onResize();
					});
			}
			const value = Reflect.get(target, prop, receiver);
			return typeof value === "function" ? value.bind(target) : value;
		},
	});
}
