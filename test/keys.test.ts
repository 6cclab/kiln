import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { createKeyRouter, DOUBLE_PRESS_MS, type KeyActions } from "../src/tui/keys.ts";

/**
 * Byte sequences, not key ids.
 *
 * `KEY` holds pi-tui key IDS now ("ctrl+r"), because the router matches through
 * `matchesKey` rather than comparing bytes. Tests must send what a terminal
 * sends, or they assert on a string no keyboard produces.
 */
const SEND = {
	ctrlC: "\x03",
	ctrlD: "\x04",
	ctrlL: "\x0c",
	ctrlR: "\x12",
	esc: "\x1b",
	shiftTab: "\x1b[Z",
} as const;

/**
 * Global key routing.
 *
 * Extracted from `runApp` precisely so this file could exist: while the router
 * was a closure over a dozen locals, the only way to check that Ctrl+C twice
 * exits was to run a session and press it. Three bindings in the parity table
 * had never been implemented, and nothing noticed.
 */

interface Recorder extends KeyActions {
	calls: string[];
}

function actions(over: Partial<KeyActions> = {}): Recorder {
	const calls: string[] = [];
	const record = (name: string) => () => {
		calls.push(name);
	};
	return {
		calls,
		isBusy: () => false,
		hasInput: () => false,
		interrupt: record("interrupt"),
		toggleExpanded: record("toggleExpanded"),
		cyclePermissionMode: record("cyclePermissionMode"),
		clearInput: record("clearInput"),
		clearScreen: record("clearScreen"),
		rewind: record("rewind"),
		exit: record("exit"),
		hint: () => calls.push("hint"),
		...over,
	};
}

/** A router with a clock the test controls, so timing needs no waiting. */
function routed(over: Partial<KeyActions> = {}) {
	const a = actions(over);
	let clock = 10_000;
	const route = createKeyRouter({ ...a, now: () => clock });
	return { a, route, advance: (ms: number) => (clock += ms) };
}

/**
 * Every binding is exercised in all three wire encodings.
 *
 * The bug this prevents: the router compared raw bytes, so `"\x1b[Z"` matched
 * and the Kitty and modifyOtherKeys forms did not. pi-tui ENABLES the Kitty
 * protocol where the terminal supports it, so the encoding that never arrived
 * was the one being matched — Shift+Tab did nothing on exactly the terminals
 * the harness runs best on.
 */
const ENCODINGS: Record<string, string[]> = {
	"shift+tab": ["\x1b[Z", "\x1b[9;2u", "\x1b[27;2;9~"],
	"ctrl+c": ["\x03", "\x1b[99;5u"],
	"ctrl+d": ["\x04", "\x1b[100;5u"],
	"ctrl+l": ["\x0c", "\x1b[108;5u"],
	"ctrl+r": ["\x12", "\x1b[114;5u"],
	escape: ["\x1b", "\x1b[27;1u"],
};

describe("every encoding of a key reaches the same action", () => {
	const expected: Record<string, string> = {
		"shift+tab": "cyclePermissionMode",
		"ctrl+l": "clearScreen",
		"ctrl+r": "toggleExpanded",
		"ctrl+d": "exit",
	};

	for (const [key, action] of Object.entries(expected)) {
		for (const encoding of ENCODINGS[key]) {
			it(`${key} as ${JSON.stringify(encoding)} triggers ${action}`, () => {
				const { a, route } = routed();
				assert.deepEqual(route(encoding), { consume: true }, "not consumed");
				assert.ok(a.calls.includes(action), `got ${a.calls.join(",") || "nothing"}`);
			});
		}
	}

	it("interrupts on every encoding of Esc while busy", () => {
		for (const encoding of ENCODINGS.escape) {
			const { a, route } = routed({ isBusy: () => true });
			route(encoding);
			assert.ok(a.calls.includes("interrupt"), `${JSON.stringify(encoding)} did not interrupt`);
		}
	});

	it("clears input on every encoding of Ctrl+C", () => {
		for (const encoding of ENCODINGS["ctrl+c"]) {
			const { a, route } = routed();
			route(encoding);
			assert.ok(a.calls.includes("clearInput"), `${JSON.stringify(encoding)} did nothing`);
		}
	});

	it("ignores key RELEASE events, which the Kitty protocol also reports", () => {
		// Acting on both press and release fires every binding twice: Ctrl+R
		// would expand and immediately collapse, reading as a dead key.
		const { a, route } = routed();
		route("\x1b[114;5u");
		const afterPress = a.calls.length;
		route("\x1b[114;5:3u");
		assert.equal(a.calls.length, afterPress, "a key release triggered an action");
	});
});

describe("key routing", () => {
	it("expands tool output on Ctrl+R", () => {
		const { a, route } = routed();
		assert.deepEqual(route(SEND.ctrlR), { consume: true });
		assert.deepEqual(a.calls, ["toggleExpanded"]);
	});

	it("cycles permission mode on Shift+Tab", () => {
		const { a, route } = routed();
		assert.deepEqual(route(SEND.shiftTab), { consume: true });
		assert.deepEqual(a.calls, ["cyclePermissionMode"]);
	});

	it("clears the screen on Ctrl+L", () => {
		const { a, route } = routed();
		route(SEND.ctrlL);
		assert.deepEqual(a.calls, ["clearScreen"]);
	});

	it("gives the permission prompt the key before anything else", () => {
		// Esc during a prompt means "decline this call", not "abort the turn".
		const { a, route } = routed({ permissionKey: () => true, isBusy: () => true });
		assert.deepEqual(route(SEND.esc), { consume: true });
		assert.ok(!a.calls.includes("interrupt"), "the turn was aborted from inside a prompt");
	});

	describe("Esc", () => {
		it("interrupts a running turn", () => {
			const { a, route } = routed({ isBusy: () => true });
			assert.deepEqual(route(SEND.esc), { consume: true });
			assert.deepEqual(a.calls, ["interrupt"]);
		});

		it("is left to the editor on a single press when idle", () => {
			const { a, route } = routed();
			assert.equal(route(SEND.esc), undefined, "a single idle Esc was consumed");
			assert.deepEqual(a.calls, []);
		});

		it("rewinds on a double press when idle", () => {
			const { a, route } = routed();
			route(SEND.esc);
			assert.deepEqual(route(SEND.esc), { consume: true });
			assert.deepEqual(a.calls, ["rewind"]);
		});

		it("does not rewind when the presses are far apart", () => {
			const { a, route, advance } = routed();
			route(SEND.esc);
			advance(DOUBLE_PRESS_MS + 1);
			route(SEND.esc);
			assert.deepEqual(a.calls, []);
		});

		it("does not treat interrupt-then-Esc as a double press", () => {
			// Interrupting a turn and then pressing Esc once should not silently
			// rewind the conversation.
			const busy = { value: true };
			const a = actions({ isBusy: () => busy.value });
			const route = createKeyRouter({ ...a, now: () => 10_000 });
			route(SEND.esc);
			busy.value = false;
			route(SEND.esc);
			assert.ok(!a.calls.includes("rewind"), "rewound after an interrupt");
		});
	});

	describe("Ctrl+C", () => {
		it("clears the input rather than exiting on the first press", () => {
			// One press exiting would make a mistyped line cost the session.
			const { a, route } = routed();
			route(SEND.ctrlC);
			assert.ok(a.calls.includes("clearInput"));
			assert.ok(!a.calls.includes("exit"), "exited on the first press");
		});

		it("exits on a second press within the window", () => {
			const { a, route } = routed();
			route(SEND.ctrlC);
			route(SEND.ctrlC);
			assert.ok(a.calls.includes("exit"));
		});

		it("does not exit when the presses are far apart", () => {
			const { a, route, advance } = routed();
			route(SEND.ctrlC);
			advance(DOUBLE_PRESS_MS + 1);
			route(SEND.ctrlC);
			assert.ok(!a.calls.includes("exit"));
		});

		it("interrupts a running turn instead of clearing the input", () => {
			const { a, route } = routed({ isBusy: () => true });
			route(SEND.ctrlC);
			assert.ok(a.calls.includes("interrupt"));
			assert.ok(!a.calls.includes("clearInput"));
		});

		it("says that a second press exits, rather than leaving it to be guessed", () => {
			const { a, route } = routed();
			route(SEND.ctrlC);
			assert.ok(a.calls.includes("hint"));
		});
	});

	describe("Ctrl+D", () => {
		it("exits on an empty line", () => {
			const { a, route } = routed();
			assert.deepEqual(route(SEND.ctrlD), { consume: true });
			assert.ok(a.calls.includes("exit"));
		});

		it("leaves a line with text to the editor", () => {
			// On a non-empty line Ctrl+D is delete-forward; exiting would throw
			// away what was typed.
			const { a, route } = routed({ hasInput: () => true });
			assert.equal(route(SEND.ctrlD), undefined);
			assert.ok(!a.calls.includes("exit"));
		});
	});

	it("passes ordinary typing straight through", () => {
		const { a, route } = routed();
		for (const ch of ["a", "Z", " ", "\r", "\t"]) {
			assert.equal(route(ch), undefined, `${JSON.stringify(ch)} was consumed`);
		}
		assert.deepEqual(a.calls, []);
	});
});
