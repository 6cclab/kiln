import { spawn } from "node:child_process";
import { DEFAULT_HOOK_TIMEOUT_SECONDS, hooksFor, type HookConfig, type HookEvent } from "./hooks.ts";

/**
 * Running hooks.
 *
 * The payload goes in on stdin as JSON; the hook may answer on stdout. Three
 * answer shapes exist, and all three are in use in this user's own config:
 *
 *   1. **Nothing** (exit 0, no stdout) — the common case. Observe and move on.
 *   2. **Plain text** — becomes additional context for the turn. The relay
 *      inbox hook `curl`s a summary and writes it raw, so stdout must never be
 *      *required* to parse as JSON.
 *   3. **JSON with `hookSpecificOutput`** — a decision. `permissionDecision`
 *      can allow or deny the call, and `updatedInput` REWRITES the tool's
 *      arguments before it runs. That is what `rtk-rewrite.sh` does on every
 *      Bash call.
 *
 * Shape 3 is why this code has to be careful: a mistake here silently executes
 * a different command than the model asked for.
 */

export interface HookPayload {
	session_id: string;
	transcript_path?: string;
	cwd: string;
	hook_event_name: HookEvent;
	tool_name?: string;
	tool_input?: Record<string, unknown>;
	tool_response?: unknown;
	prompt?: string;
	reason?: string;
}

export interface HookOutcome {
	/** Replacement tool arguments, when a hook rewrote them. */
	updatedInput?: Record<string, unknown>;
	/** Set when a hook refused the call. */
	blocked?: { reason: string };
	/** Text to add to the model's context. */
	context: string[];
	/** Messages for the user, never for the model. */
	notices: string[];
}

interface HookSpecificOutput {
	hookEventName?: string;
	permissionDecision?: "allow" | "deny" | "ask";
	permissionDecisionReason?: string;
	updatedInput?: Record<string, unknown>;
	additionalContext?: string;
}

interface RunOutput {
	code: number | null;
	stdout: string;
	stderr: string;
	timedOut: boolean;
}

function runCommand(command: string, input: string, timeoutSeconds: number, cwd: string): Promise<RunOutput> {
	return new Promise((resolve) => {
		// Through a shell, because the configured commands are shell one-liners
		// (`t=$(cat ...) || exit 0; curl ... || true`), not argv arrays.
		const child = spawn(command, {
			shell: true,
			cwd,
			// Its own process group, so the timeout can kill the whole tree.
			// `shell: true` means the child is `/bin/sh -c "<command>"`; killing
			// only that leaves its children alive, still holding the stdout pipe,
			// so `close` never fires and the timeout does not actually time out.
			// Measured: a 1-second timeout on `sleep 30` took the full 30s.
			detached: true,
			// The hook's own env plus a marker, so a hook that shells back into
			// the harness can tell it is being re-entered. The kb-capture hook
			// relies on exactly this pattern to avoid recursing forever.
			env: { ...process.env, CLAUDE_HOOK: "1", HARNESS_HOOK: "1" },
			stdio: ["pipe", "pipe", "pipe"],
		});

		let stdout = "";
		let stderr = "";
		let timedOut = false;

		const timer = setTimeout(() => {
			timedOut = true;
			// Negative pid targets the group, which is the point of `detached`.
			try {
				if (child.pid !== undefined) process.kill(-child.pid, "SIGKILL");
			} catch {
				// Already gone, or no group. Fall back to the child itself.
				child.kill("SIGKILL");
			}
		}, timeoutSeconds * 1000);

		child.stdout.on("data", (d) => {
			stdout += String(d);
		});
		child.stderr.on("data", (d) => {
			stderr += String(d);
		});

		child.on("error", (err) => {
			clearTimeout(timer);
			// A missing interpreter or unreadable script is a failed hook, not a
			// failed session.
			resolve({ code: 1, stdout, stderr: String(err), timedOut });
		});

		child.on("close", (code) => {
			clearTimeout(timer);
			resolve({ code, stdout, stderr, timedOut });
		});

		child.stdin.on("error", () => {
			// A hook that exits without reading stdin (the common `exit 0` guard
			// in rtk-rewrite.sh) gives us EPIPE. Expected, not an error.
		});
		child.stdin.end(input);
	});
}

/** Parse stdout, which may be JSON, plain text, or nothing. */
function interpret(out: RunOutput, outcome: HookOutcome, label: string): void {
	const text = out.stdout.trim();

	if (out.timedOut) {
		outcome.notices.push(`hook timed out: ${label}`);
		return;
	}

	// Exit 2 is Claude Code's "block", with the reason on stderr. Checked before
	// stdout is parsed: a blocking hook's stdout is not a decision document.
	if (out.code === 2) {
		outcome.blocked = { reason: out.stderr.trim() || `blocked by hook: ${label}` };
		return;
	}

	if (out.code !== 0) {
		// Non-blocking failure. Surfaced rather than swallowed - a hook that
		// stopped working should not look like a hook that chose to do nothing.
		if (out.stderr.trim()) outcome.notices.push(`hook failed (${out.code}): ${out.stderr.trim()}`);
		return;
	}

	if (out.stderr.trim()) outcome.notices.push(out.stderr.trim());
	if (!text) return;

	let parsed: { hookSpecificOutput?: HookSpecificOutput; continue?: boolean; stopReason?: string } | undefined;
	try {
		const value = JSON.parse(text);
		if (value && typeof value === "object") parsed = value;
	} catch {
		// Not JSON. This is the relay-inbox case: raw text is context.
	}

	if (!parsed) {
		outcome.context.push(text);
		return;
	}

	if (parsed.continue === false) {
		outcome.blocked = { reason: parsed.stopReason || `stopped by hook: ${label}` };
		return;
	}

	const specific = parsed.hookSpecificOutput;
	if (!specific) return;

	if (specific.permissionDecision === "deny") {
		outcome.blocked = { reason: specific.permissionDecisionReason || `denied by hook: ${label}` };
		return;
	}
	if (specific.updatedInput && typeof specific.updatedInput === "object") {
		// Merged into whatever an earlier hook already rewrote, so two hooks
		// touching different fields compose instead of the last one winning.
		outcome.updatedInput = { ...outcome.updatedInput, ...specific.updatedInput };
	}
	if (specific.additionalContext) outcome.context.push(specific.additionalContext);
}

export interface RunHooksOptions {
	config: HookConfig;
	event: HookEvent;
	payload: Omit<HookPayload, "hook_event_name">;
	toolName?: string;
	onNotice?: (message: string) => void;
}

/**
 * Run every hook registered for an event, in configured order.
 *
 * Sequential on purpose. Hooks rewrite the same `tool_input`, so running them
 * concurrently would make the result depend on which finished first — and the
 * one hook in play here rewrites the command that is about to execute.
 */
export async function runHooks(opts: RunHooksOptions): Promise<HookOutcome> {
	const outcome: HookOutcome = { context: [], notices: [] };
	const hooks = hooksFor(opts.config, opts.event, opts.toolName);
	if (hooks.length === 0) return outcome;

	const payload: HookPayload = { ...opts.payload, hook_event_name: opts.event };

	for (const hook of hooks) {
		// Each hook sees the input as rewritten by the ones before it, which is
		// what makes a chain of rewrites compose rather than conflict.
		const current: HookPayload = outcome.updatedInput
			? { ...payload, tool_input: { ...payload.tool_input, ...outcome.updatedInput } }
			: payload;

		const out = await runCommand(
			hook.command,
			JSON.stringify(current),
			hook.timeout ?? DEFAULT_HOOK_TIMEOUT_SECONDS,
			opts.payload.cwd,
		);

		const before = outcome.notices.length;
		interpret(out, outcome, hook.command.slice(0, 60));
		for (const notice of outcome.notices.slice(before)) opts.onNotice?.(notice);

		// A block ends the chain: later hooks have nothing left to decide.
		if (outcome.blocked) break;
	}

	return outcome;
}

export interface GuardOptions {
	config: HookConfig;
	toolName: string;
	args: Record<string, unknown>;
	sessionId: string;
	transcriptPath?: string;
	cwd: string;
	/** The permission check, already bound to a gate. */
	check: (req: {
		toolName: string;
		primaryArg: string | undefined;
		args: Record<string, unknown>;
	}) => Promise<{ reason: string } | undefined>;
	primaryArgOf: (args: Record<string, unknown>) => string | undefined;
	onNotice?: (message: string) => void;
}

export interface GuardResult {
	blocked?: { reason: string };
	/** Present only when a hook rewrote the call. */
	args?: Record<string, unknown>;
}

/**
 * Compose PreToolUse hooks with the permission gate.
 *
 * **Hooks run first, then the gate.** That order is a safety property, not a
 * preference. A hook may rewrite the command — the rtk hook on this machine
 * turns `git status` into `rtk git status` on every Bash call — and the gate
 * has to judge what will actually execute, not what the model proposed.
 *
 * Gate-then-hooks would let any rewrite escape every rule: a `deny` on the
 * rewritten form would never match, because the gate would only ever have seen
 * the original.
 *
 * Extracted from the harness wiring so this ordering can be tested directly.
 * The end-to-end path is not a reliable place to check it: the rtk hook
 * silently stops rewriting when a project `.claude/settings.local.json` exists,
 * so adding a deny rule to test the interaction disables the rewrite being
 * tested.
 */
export async function guardToolCall(opts: GuardOptions): Promise<GuardResult> {
	let args = opts.args;

	const hookResult = await runHooks({
		config: opts.config,
		event: "PreToolUse",
		toolName: opts.toolName,
		payload: {
			session_id: opts.sessionId,
			transcript_path: opts.transcriptPath,
			cwd: opts.cwd,
			tool_name: opts.toolName,
			tool_input: args,
		},
		onNotice: opts.onNotice,
	});

	if (hookResult.blocked) return { blocked: hookResult.blocked };

	if (hookResult.updatedInput) {
		args = { ...args, ...hookResult.updatedInput };
		opts.onNotice?.(`rewrote ${opts.toolName}: ${opts.primaryArgOf(args) ?? ""}`);
	}

	const blocked = await opts.check({
		toolName: opts.toolName,
		primaryArg: opts.primaryArgOf(args),
		args,
	});
	if (blocked) return { blocked };

	return hookResult.updatedInput ? { args } : {};
}
