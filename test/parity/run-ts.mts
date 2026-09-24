#!/usr/bin/env -S node --experimental-strip-types --no-warnings
/**
 * TS side of the parity oracle.
 *
 * Boots the real `runApp` (src/tui/app.ts) against a real `fauxProvider()`
 * from @earendil-works/pi-ai, driven by a `FakeTerminal` + `Screen`
 * (test/support/*), and plays back a tiny action script (the same
 * SEND/KEY/WAIT/SLEEP/SCREEN/RESIZE/EXIT line language cmd/harness-drive
 * speaks on the Go side, so both sides are driven identically).
 *
 * `runApp` hardcodes `new ProcessTerminal()`. To run it in-process at all,
 * src/tui/app.ts was given one additive seam: an optional `opts.terminal`
 * that defaults to a real ProcessTerminal when absent (see AppOptions).
 * Nothing else about the component stack or its behavior changed - this is
 * the same runApp production runs, with its output device swapped for
 * one connected to a real @xterm/headless VT so a screen dump is
 * possible without a tty.
 *
 * Usage:
 *   node --experimental-strip-types --no-warnings test/parity/run-ts.mts \
 *     --faux path/to/script.yaml --actions path/to/actions.txt \
 *     --cols 100 --rows 30 --cwd <dir> --home <dir> --sessions-dir <dir> \
 *     --permission-mode manual --allowed-tools Read,Bash
 *
 * Prints one JSON line per SCREEN action to stdout:
 *   {"rows":[...],"cursorRow":N,"occupiedHeight":N,"cols":C,"rowsCount":R}
 *
 * Diagnostics go to stderr only, so stdout stays a clean line-per-screen
 * stream the Go runner can parse.
 */

import { readFile } from "node:fs/promises";
import { parse as parseYaml } from "yaml";
import { fauxAssistantMessage, fauxText, fauxThinking, fauxToolCall, fauxProvider } from "@earendil-works/pi-ai/providers/faux";
import type { FauxResponseStep } from "@earendil-works/pi-ai/providers/faux";
import { createRegistry } from "../../src/provider/registry.ts";
import { startSession } from "../../src/agent/session.ts";
import { CommandRegistry } from "../../src/commands/registry.ts";
import { PermissionGate, primaryArgOf } from "../../src/claude/permission.ts";
import { runApp } from "../../src/tui/app.ts";
import { NodeExecutionEnv } from "@earendil-works/pi-agent-core/node";
import { Screen } from "../support/screen.ts";

// --- arg parsing -----------------------------------------------------------

function parseArgv(argv: string[]): Record<string, string> {
	const out: Record<string, string> = {};
	for (let i = 0; i < argv.length; i++) {
		const a = argv[i];
		if (!a.startsWith("--")) continue;
		const key = a.slice(2);
		const next = argv[i + 1];
		if (next === undefined || next.startsWith("--")) {
			out[key] = "true";
		} else {
			out[key] = next;
			i++;
		}
	}
	return out;
}

const args = parseArgv(process.argv.slice(2));
const cols = Number(args.cols ?? 100);
const rows = Number(args.rows ?? 30);
const cwd = args.cwd ?? process.cwd();
const home = args.home ?? cwd;
const sessionsDir = args["sessions-dir"] ?? cwd;
const permissionMode = (args["permission-mode"] ?? "acceptEdits") as
	| "manual"
	| "acceptEdits"
	| "auto"
	| "plan"
	| "bypassPermissions";
const allowedTools = (args["allowed-tools"] ?? "")
	.split(",")
	.map((s) => s.trim())
	.filter(Boolean);
process.env.HOME = home;

function log(...a: unknown[]): void {
	console.error("[run-ts]", ...a);
}

// --- faux YAML -> ordered fauxProvider responses ----------------------------
//
// Mirrors internal/testkit/faux/script.go's Script/Step/flattenSteps: a
// nested steps list where a plain run of {text,thinking,tool_call,usage}
// entries accumulates into one turn, a tool_call ends the turn immediately,
// and `on_tool_result: <id>` / `then: [...]` describes the NEXT turn (gated,
// on the Go HTTP faux server, on that tool result actually arriving).
//
// pi-ai's fauxProvider has no such gate: FauxProviderRegistration.setResponses
// queues responses and `stream()` does a plain `pendingResponses.shift()` per
// model call (see node_modules/@earendil-works/pi-ai/dist/providers/faux.js).
// So the `on_tool_result` id is NOT enforced here - only the ORDER matters,
// which is fine because the harness always sends exactly one request per
// completed tool call, in the order the script describes. This is the one
// place the TS and Go oracles use genuinely different mechanisms for the same
// intent; flagged in the parity report's step-mapping table.
//
// `usage` and `delay` steps also have no TS equivalent: fauxProvider ignores
// scripted usage and re-estimates token counts from content length
// (withUsageEstimate in faux.js), and there is no per-step delay knob (only a
// global tokensPerSecond). Both are reported as diff classes, not silently
// dropped.

interface YamlStep {
	text?: string;
	thinking?: string;
	tool_call?: { name: string; args?: Record<string, unknown>; id?: string };
	on_tool_result?: string;
	then?: YamlStep[];
	usage?: { input: number; output: number };
	error?: { status: number; type: string; message: string };
	delay?: string;
}

interface ContentStep {
	text?: string;
	thinking?: string;
	toolCall?: { name: string; args: Record<string, unknown>; id: string };
}

interface Turn {
	isError: boolean;
	errorMessage?: string;
	content: ContentStep[];
	hasToolCall: boolean;
}

function toContentStep(s: YamlStep): ContentStep {
	const cs: ContentStep = {};
	if (s.text) cs.text = s.text;
	if (s.thinking) cs.thinking = s.thinking;
	if (s.tool_call) cs.toolCall = { name: s.tool_call.name, args: s.tool_call.args ?? {}, id: s.tool_call.id ?? "tc" };
	return cs;
}

function flattenSteps(steps: YamlStep[]): Turn[] {
	const turns: Turn[] = [];
	let pending: ContentStep[] = [];

	const flush = () => {
		if (pending.length > 0) {
			turns.push({ isError: false, content: pending, hasToolCall: pending.some((c) => c.toolCall) });
			pending = [];
		}
	};

	for (const s of steps) {
		if (s.error) {
			flush();
			turns.push({ isError: true, errorMessage: s.error.message, content: [], hasToolCall: false });
			continue;
		}
		if (s.on_tool_result !== undefined) {
			flush();
			const inner = flattenSteps(s.then ?? []);
			turns.push(...inner);
			continue;
		}
		const cs = toContentStep(s);
		pending.push(cs);
		if (s.tool_call) flush();
	}
	flush();
	return turns;
}

async function loadFauxResponses(path: string): Promise<FauxResponseStep[]> {
	const raw = await readFile(path, "utf8");
	const doc = parseYaml(raw) as { model?: string; steps?: YamlStep[] };
	const turns = flattenSteps(doc.steps ?? []);

	return turns.map((t): FauxResponseStep => {
		if (t.isError) {
			return fauxAssistantMessage([], { stopReason: "error", errorMessage: t.errorMessage });
		}
		const blocks = t.content.flatMap((c) => {
			const out = [] as ReturnType<typeof fauxText>[] | ReturnType<typeof fauxThinking>[] | ReturnType<typeof fauxToolCall>[];
			const parts: (ReturnType<typeof fauxText> | ReturnType<typeof fauxThinking> | ReturnType<typeof fauxToolCall>)[] = [];
			if (c.thinking) parts.push(fauxThinking(c.thinking));
			if (c.text) parts.push(fauxText(c.text));
			if (c.toolCall) parts.push(fauxToolCall(c.toolCall.name, c.toolCall.args, { id: c.toolCall.id }));
			return parts;
		});
		return fauxAssistantMessage(blocks.length > 0 ? blocks : [fauxText("")], {
			stopReason: t.hasToolCall ? "toolUse" : "stop",
		});
	});
}

// --- action script -----------------------------------------------------------

const KEY_BYTES: Record<string, string> = {
	enter: "\r",
	escape: "\x1b",
	esc: "\x1b",
	tab: "\t",
	"shift+tab": "\x1b[Z",
	backspace: "\x7f",
	up: "\x1b[A",
	down: "\x1b[B",
	left: "\x1b[D",
	right: "\x1b[C",
	home: "\x1b[H",
	end: "\x1b[F",
	pgup: "\x1b[5~",
	pgdown: "\x1b[6~",
	delete: "\x1b[3~",
	insert: "\x1b[2~",
	space: " ",
};

function keyBytes(name: string): string {
	if (KEY_BYTES[name]) return KEY_BYTES[name];
	if (name.startsWith("ctrl+") && name.length === 6) {
		const c = name.charCodeAt(5);
		return String.fromCharCode(c & 0x1f);
	}
	if (name.startsWith("alt+") && name.length === 5) {
		return `\x1b${name[4]}`;
	}
	if (name.length === 1) return name;
	throw new Error(`unknown key name ${JSON.stringify(name)}`);
}

function unescape(s: string): string {
	return s.replace(/\\([rnte])/g, (_, c) => ({ r: "\r", n: "\n", t: "\t", e: "\x1b" })[c as "r" | "n" | "t" | "e"]);
}

async function waitFor(screen: Screen, pattern: string, timeoutMs: number): Promise<void> {
	const isRegex = pattern.startsWith("/") && pattern.endsWith("/") && pattern.length >= 2;
	const re = isRegex ? new RegExp(pattern.slice(1, -1)) : undefined;
	const deadline = Date.now() + timeoutMs;
	for (;;) {
		await screen.settle(20);
		const text = screen.text();
		if (re ? re.test(text) : text.includes(pattern)) return;
		if (Date.now() > deadline) {
			throw new Error(`WAIT timed out after ${timeoutMs}ms waiting for ${JSON.stringify(pattern)}\n--- screen ---\n${text}`);
		}
	}
}

function parseDuration(s: string): number {
	const m = /^(\d+)(ms|s|m)$/.exec(s.trim());
	if (!m) throw new Error(`invalid duration ${JSON.stringify(s)}`);
	const n = Number(m[1]);
	return m[2] === "ms" ? n : m[2] === "s" ? n * 1000 : n * 60_000;
}

function printScreen(screen: Screen): void {
	const rows = screen.viewport();
	const cursorRow = screen.cursorRow();
	let occupiedHeight = 0;
	for (let i = 0; i < rows.length; i++) if (rows[i].trimEnd() !== "") occupiedHeight = i + 1;
	console.log(
		JSON.stringify({
			rows,
			cursorRow,
			occupiedHeight,
			cols,
			rowsCount: rows.length,
		}),
	);
}

async function runActions(screen: Screen, actionsPath: string): Promise<void> {
	const text = await readFile(actionsPath, "utf8");
	for (const rawLine of text.split("\n")) {
		const line = rawLine.trimEnd();
		if (!line.trim() || line.trim().startsWith("#")) continue;
		const sp = line.indexOf(" ");
		const cmd = (sp === -1 ? line : line.slice(0, sp)).toUpperCase();
		const rest = sp === -1 ? "" : line.slice(sp + 1).trim();

		log("action", cmd, rest);
		switch (cmd) {
			case "SEND":
				screen.term.send(unescape(rest));
				await screen.settle(30);
				break;
			case "KEY": {
				for (const name of rest.split(/\s+/).filter(Boolean)) screen.term.send(keyBytes(name));
				await screen.settle(30);
				break;
			}
			case "WAIT": {
				let timeout = 5000;
				let pattern = rest;
				const fields = rest.split(/\s+/);
				if (fields.length > 1 && /^\d+(ms|s|m)$/.test(fields[0])) {
					timeout = parseDuration(fields[0]);
					pattern = rest.slice(fields[0].length).trim();
				}
				await waitFor(screen, pattern, timeout);
				break;
			}
			case "SLEEP":
				await new Promise((r) => setTimeout(r, parseDuration(rest)));
				await screen.settle(10);
				break;
			case "SCREEN":
				await screen.settle(10);
				printScreen(screen);
				break;
			case "RESIZE": {
				const [w, h] = rest.split(/\s+/).map(Number);
				screen.term.resize(w, h);
				await screen.settle(30);
				break;
			}
			case "EXIT":
				return;
			default:
				throw new Error(`unknown action ${JSON.stringify(cmd)}`);
		}
	}
}

// --- boot --------------------------------------------------------------------

async function main(): Promise<void> {
	if (!args.faux) throw new Error("--faux <script.yaml> is required");
	if (!args.actions) throw new Error("--actions <file> is required");

	const responses = await loadFauxResponses(args.faux);
	log(`loaded ${responses.length} faux turn(s) from ${args.faux}`);

	const faux = fauxProvider();
	faux.setResponses(responses);

	const reg = createRegistry();
	reg.models.setProvider(faux.provider);
	const resolved = await reg.resolve("faux", "faux-1");

	const env = new NodeExecutionEnv({ cwd });

	const permissionGate = new PermissionGate({
		permissions: { allow: allowedTools, deny: [], ask: [] },
		mode: permissionMode,
		roots: [cwd],
	});

	const session = await startSession({ registry: reg, resolved, cwd, sessionsDir });

	// Same wiring cli.ts does for `before_tool`, minus the .claude/settings.json
	// hook layer (out of scope for the fixtures this oracle covers).
	session.harness.hooks.on("before_tool", async (event: unknown) => {
		const e = event as { toolName: string; args?: Record<string, unknown> };
		const args_ = e.args ?? {};
		const blocked = await permissionGate.check({
			toolName: e.toolName,
			primaryArg: primaryArgOf(args_),
			args: args_,
		});
		return blocked ? { block: { reason: blocked.reason } } : undefined;
	});

	const screen = new Screen(cols, rows);
	const commands = new CommandRegistry();

	const appDone = runApp({
		session,
		commands,
		modelLabel: "faux/faux-1",
		cwd,
		env,
		gate: permissionGate,
		terminal: screen.term,
	}).catch((err) => {
		log("runApp rejected:", err);
	});

	await screen.settle(80);
	await runActions(screen, args.actions);

	// runApp never resolves on its own (it waits on stdin) - the process exit
	// below is how the harness itself terminates a real session. Here EXIT (or
	// running out of actions) just ends the script; the process exit stops the
	// event loop.
	void appDone;
	process.exit(0);
}

main().catch((err) => {
	console.error("[run-ts] fatal:", err);
	process.exit(1);
});
