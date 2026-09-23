/**
 * Global key routing (parity spec §6).
 *
 * Extracted from `runApp` so it can be tested. It was a closure over a dozen
 * locals, which meant the only way to exercise Ctrl+C-twice-exits was to run an
 * interactive session and press it — so nobody did, and three of the bindings
 * in the parity table were never implemented at all.
 *
 * Ordering is the whole design here. Each binding is checked before the editor
 * sees the byte, and the order encodes what a key means *right now*:
 *
 *   - A permission prompt owns the keyboard while it is up. `Esc` there means
 *     "decline this call", not "abort the turn".
 *   - `Esc` during a running turn means interrupt. Only when idle does a second
 *     `Esc` mean rewind.
 *   - `Ctrl+C` clears the input; a second one within the window exits. One
 *     press exiting would make a mistyped line cost the session.
 */

/** Control bytes, named. `"\x12"` at a call site is unreadable. */
export const KEY = {
	ctrlC: "\x03",
	ctrlD: "\x04",
	ctrlL: "\x0c",
	ctrlR: "\x12",
	esc: "\x1b",
	/** CSI Z. */
	shiftTab: "\x1b[Z",
} as const;

/** How long a second press still counts as a double press. */
export const DOUBLE_PRESS_MS = 1000;

export interface KeyActions {
	/** A permission prompt is up and wants the key. Returns true if it took it. */
	permissionKey?: (data: string) => boolean;
	/** A turn is running. */
	isBusy: () => boolean;
	/** Whether the input line currently has text. */
	hasInput: () => boolean;

	interrupt: () => void;
	toggleExpanded: () => void;
	cyclePermissionMode: () => void;
	clearInput: () => void;
	clearScreen: () => void;
	rewind: () => void;
	exit: () => void;
	/** A transient message for the footer, e.g. "press ctrl+c again to exit". */
	hint: (message: string) => void;
}

export interface KeyRouterOptions extends KeyActions {
	/** Injected so double-press timing is testable without waiting. */
	now?: () => number;
}

export type KeyResult = { consume: true } | undefined;

export function createKeyRouter(opts: KeyRouterOptions): (data: string) => KeyResult {
	const now = opts.now ?? (() => Date.now());
	let lastCtrlC = 0;
	let lastEsc = 0;

	return (data: string): KeyResult => {
		// The prompt owns the keyboard while it is up, ahead of everything else.
		if (opts.permissionKey?.(data)) return { consume: true };

		switch (data) {
			case KEY.ctrlR:
				opts.toggleExpanded();
				return { consume: true };

			case KEY.shiftTab:
				opts.cyclePermissionMode();
				return { consume: true };

			case KEY.ctrlL:
				opts.clearScreen();
				return { consume: true };

			case KEY.ctrlC: {
				const at = now();
				if (at - lastCtrlC < DOUBLE_PRESS_MS) {
					opts.exit();
					return { consume: true };
				}
				lastCtrlC = at;
				// Interrupting counts as the useful thing to do first; the second
				// press still exits.
				if (opts.isBusy()) opts.interrupt();
				else opts.clearInput();
				opts.hint("press ctrl+c again to exit");
				return { consume: true };
			}

			case KEY.ctrlD:
				// Only on an empty line: on a line with text, Ctrl+D is a
				// delete-forward the editor should handle, and exiting instead
				// would lose what was typed.
				if (opts.hasInput()) return undefined;
				opts.exit();
				return { consume: true };

			case KEY.esc: {
				if (opts.isBusy()) {
					opts.interrupt();
					// Consumed: while a turn runs Esc means interrupt, not "clear
					// the input line".
					return { consume: true };
				}
				const at = now();
				if (at - lastEsc < DOUBLE_PRESS_MS) {
					lastEsc = 0;
					opts.rewind();
					return { consume: true };
				}
				lastEsc = at;
				// Not consumed: a single Esc when idle still belongs to the editor.
				return undefined;
			}

			default:
				return undefined;
		}
	};
}
