import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { createKeyRouter, DOUBLE_PRESS_MS, KEY, type KeyActions } from "../src/tui/keys.ts";

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

describe("key routing", () => {
	it("expands tool output on Ctrl+R", () => {
		const { a, route } = routed();
		assert.deepEqual(route(KEY.ctrlR), { consume: true });
		assert.deepEqual(a.calls, ["toggleExpanded"]);
	});

	it("cycles permission mode on Shift+Tab", () => {
		const { a, route } = routed();
		assert.deepEqual(route(KEY.shiftTab), { consume: true });
		assert.deepEqual(a.calls, ["cyclePermissionMode"]);
	});

	it("clears the screen on Ctrl+L", () => {
		const { a, route } = routed();
		route(KEY.ctrlL);
		assert.deepEqual(a.calls, ["clearScreen"]);
	});

	it("gives the permission prompt the key before anything else", () => {
		// Esc during a prompt means "decline this call", not "abort the turn".
		const { a, route } = routed({ permissionKey: () => true, isBusy: () => true });
		assert.deepEqual(route(KEY.esc), { consume: true });
		assert.ok(!a.calls.includes("interrupt"), "the turn was aborted from inside a prompt");
	});

	describe("Esc", () => {
		it("interrupts a running turn", () => {
			const { a, route } = routed({ isBusy: () => true });
			assert.deepEqual(route(KEY.esc), { consume: true });
			assert.deepEqual(a.calls, ["interrupt"]);
		});

		it("is left to the editor on a single press when idle", () => {
			const { a, route } = routed();
			assert.equal(route(KEY.esc), undefined, "a single idle Esc was consumed");
			assert.deepEqual(a.calls, []);
		});

		it("rewinds on a double press when idle", () => {
			const { a, route } = routed();
			route(KEY.esc);
			assert.deepEqual(route(KEY.esc), { consume: true });
			assert.deepEqual(a.calls, ["rewind"]);
		});

		it("does not rewind when the presses are far apart", () => {
			const { a, route, advance } = routed();
			route(KEY.esc);
			advance(DOUBLE_PRESS_MS + 1);
			route(KEY.esc);
			assert.deepEqual(a.calls, []);
		});

		it("does not treat interrupt-then-Esc as a double press", () => {
			// Interrupting a turn and then pressing Esc once should not silently
			// rewind the conversation.
			const busy = { value: true };
			const a = actions({ isBusy: () => busy.value });
			const route = createKeyRouter({ ...a, now: () => 10_000 });
			route(KEY.esc);
			busy.value = false;
			route(KEY.esc);
			assert.ok(!a.calls.includes("rewind"), "rewound after an interrupt");
		});
	});

	describe("Ctrl+C", () => {
		it("clears the input rather than exiting on the first press", () => {
			// One press exiting would make a mistyped line cost the session.
			const { a, route } = routed();
			route(KEY.ctrlC);
			assert.ok(a.calls.includes("clearInput"));
			assert.ok(!a.calls.includes("exit"), "exited on the first press");
		});

		it("exits on a second press within the window", () => {
			const { a, route } = routed();
			route(KEY.ctrlC);
			route(KEY.ctrlC);
			assert.ok(a.calls.includes("exit"));
		});

		it("does not exit when the presses are far apart", () => {
			const { a, route, advance } = routed();
			route(KEY.ctrlC);
			advance(DOUBLE_PRESS_MS + 1);
			route(KEY.ctrlC);
			assert.ok(!a.calls.includes("exit"));
		});

		it("interrupts a running turn instead of clearing the input", () => {
			const { a, route } = routed({ isBusy: () => true });
			route(KEY.ctrlC);
			assert.ok(a.calls.includes("interrupt"));
			assert.ok(!a.calls.includes("clearInput"));
		});

		it("says that a second press exits, rather than leaving it to be guessed", () => {
			const { a, route } = routed();
			route(KEY.ctrlC);
			assert.ok(a.calls.includes("hint"));
		});
	});

	describe("Ctrl+D", () => {
		it("exits on an empty line", () => {
			const { a, route } = routed();
			assert.deepEqual(route(KEY.ctrlD), { consume: true });
			assert.ok(a.calls.includes("exit"));
		});

		it("leaves a line with text to the editor", () => {
			// On a non-empty line Ctrl+D is delete-forward; exiting would throw
			// away what was typed.
			const { a, route } = routed({ hasInput: () => true });
			assert.equal(route(KEY.ctrlD), undefined);
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
