import { strict as assert } from "node:assert";
import { describe, it, before, after } from "node:test";
import { mkdtemp, mkdir, rm, writeFile, chmod } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { hooksFor, loadHooks, matchesHook, type HookConfig } from "../src/claude/hooks.ts";
import { guardToolCall, runHooks } from "../src/claude/hook-runner.ts";

describe("matchesHook", () => {
	it("matches the configured tool name regardless of casing", () => {
		// Settings are written in Claude Code's casing (`Bash`) while pi's tool
		// is `bash`. Without this the user's rtk hook never fires at all.
		assert.ok(matchesHook("Bash", "bash"));
		assert.ok(matchesHook("Read", "read"));
	});

	it("is anchored, so Bash does not match BashOutput", () => {
		// An unanchored matcher would fire a command-rewriting hook on tools it
		// was never meant to touch.
		assert.ok(!matchesHook("Bash", "bashoutput"));
		assert.ok(!matchesHook("Edit", "edit_notebook"));
	});

	it("supports the regex alternation real configs use", () => {
		assert.ok(matchesHook("Edit|Write", "write"));
		assert.ok(!matchesHook("Edit|Write", "read"));
	});

	it("treats an absent or wildcard matcher as everything", () => {
		assert.ok(matchesHook(undefined, "anything"));
		assert.ok(matchesHook("*", "anything"));
		// Events with no tool still match a wildcard group.
		assert.ok(matchesHook(undefined, undefined));
	});

	it("fails closed on a malformed regex", () => {
		// Failing open would apply a rewrite hook to every tool call.
		assert.ok(!matchesHook("[unclosed", "bash"));
	});
});

describe("loadHooks", () => {
	let dir = "";

	before(async () => {
		dir = await mkdtemp(join(tmpdir(), "harness-hooks-"));
		await mkdir(join(dir, ".claude"), { recursive: true });
		await writeFile(
			join(dir, ".claude", "settings.json"),
			JSON.stringify({ hooks: { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: "a" }] }] } }),
		);
		await writeFile(
			join(dir, ".claude", "settings.local.json"),
			JSON.stringify({ hooks: { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: "b" }] }] } }),
		);
	});

	after(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("accumulates hooks across scopes instead of overriding", async () => {
		// A project formatting hook must not silently disable the user's global
		// audit hook. Both run.
		const config = await loadHooks(dir);
		const commands = hooksFor(config, "PreToolUse", "bash").map((h) => h.command);
		assert.ok(commands.includes("a") && commands.includes("b"), `got ${commands.join(",")}`);
	});
});

describe("runHooks", () => {
	let dir = "";
	const script = (name: string) => join(dir, name);

	const write = async (name: string, body: string) => {
		const path = script(name);
		await writeFile(path, `#!/bin/bash\n${body}\n`);
		await chmod(path, 0o755);
		return path;
	};

	const config = (command: string, timeout?: number): HookConfig => ({
		PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command, ...(timeout ? { timeout } : {}) }] }],
	});

	const run = (cfg: HookConfig, toolInput: Record<string, unknown> = { command: "git status" }) =>
		runHooks({
			config: cfg,
			event: "PreToolUse",
			toolName: "bash",
			payload: { session_id: "t", cwd: dir, tool_name: "bash", tool_input: toolInput },
		});

	before(async () => {
		dir = await mkdtemp(join(tmpdir(), "harness-hookrun-"));
	});

	after(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("passes the payload in on stdin as JSON", async () => {
		// The rtk hook reads `.tool_input.command`; if the payload shape is
		// wrong it exits silently and every rewrite stops happening.
		const path = await write("echo-cmd.sh", `jq -r '.tool_input.command'`);
		const out = await run(config(path));
		assert.deepEqual(out.context, ["git status"]);
	});

	it("applies updatedInput, rewriting what will actually run", async () => {
		// The shape rtk-rewrite.sh emits.
		const path = await write(
			"rewrite.sh",
			`cat >/dev/null; jq -n '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"allow",updatedInput:{command:"rtk git status"}}}'`,
		);
		const out = await run(config(path));
		assert.deepEqual(out.updatedInput, { command: "rtk git status" });
		assert.equal(out.blocked, undefined);
	});

	it("treats non-JSON stdout as context", async () => {
		// The relay inbox hook curls a summary and writes it raw. Requiring JSON
		// would discard it.
		const path = await write("plain.sh", `cat >/dev/null; echo "you have 2 unread messages"`);
		const out = await run(config(path));
		assert.deepEqual(out.context, ["you have 2 unread messages"]);
		assert.equal(out.updatedInput, undefined);
	});

	it("blocks on exit 2 with stderr as the reason", async () => {
		const path = await write("deny.sh", `cat >/dev/null; echo "not allowed here" >&2; exit 2`);
		const out = await run(config(path));
		assert.equal(out.blocked?.reason, "not allowed here");
	});

	it("blocks on an explicit deny decision", async () => {
		const path = await write(
			"deny-json.sh",
			`cat >/dev/null; jq -n '{hookSpecificOutput:{permissionDecision:"deny",permissionDecisionReason:"policy"}}'`,
		);
		assert.equal((await run(config(path))).blocked?.reason, "policy");
	});

	it("does not block on an ordinary non-zero exit", async () => {
		// `rtk rewrite` exits 1 when there is nothing to rewrite. Treating that
		// as a block would break every bash call on this machine.
		const path = await write("fail.sh", `cat >/dev/null; echo "warning" >&2; exit 1`);
		const out = await run(config(path));
		assert.equal(out.blocked, undefined);
		assert.ok(out.notices.some((n) => n.includes("warning")));
	});

	it("survives a hook that exits without reading stdin", async () => {
		// rtk-rewrite.sh exits early when jq is missing, before reading stdin.
		// The resulting EPIPE must not surface as a failure.
		const path = await write("early-exit.sh", `exit 0`);
		const out = await run(config(path));
		assert.equal(out.blocked, undefined);
		assert.deepEqual(out.context, []);
	});

	it("kills a hook that exceeds its timeout", async () => {
		const path = await write("hang.sh", `cat >/dev/null; sleep 30`);
		const started = Date.now();
		const out = await run(config(path, 1));
		assert.ok(Date.now() - started < 10_000, "did not kill the hook");
		assert.ok(out.notices.some((n) => n.includes("timed out")));
		// A hung hook must not block the call - it decided nothing.
		assert.equal(out.blocked, undefined);
	});

	it("survives a command that does not exist", async () => {
		const out = await run(config("/nonexistent/hook.sh"));
		assert.equal(out.blocked, undefined);
	});

	it("chains rewrites, each hook seeing the previous one's output", async () => {
		const first = await write(
			"first.sh",
			`cat >/dev/null; jq -n '{hookSpecificOutput:{updatedInput:{command:"one"}}}'`,
		);
		const second = await write(
			"second.sh",
			// Reads what it was handed, proving the chain passes the rewrite on.
			`c=$(jq -r '.tool_input.command'); jq -n --arg c "$c" '{hookSpecificOutput:{updatedInput:{command:($c+"-two")}}}'`,
		);
		const out = await runHooks({
			config: {
				PreToolUse: [
					{ matcher: "Bash", hooks: [{ type: "command", command: first }] },
					{ matcher: "Bash", hooks: [{ type: "command", command: second }] },
				],
			},
			event: "PreToolUse",
			toolName: "bash",
			payload: { session_id: "t", cwd: dir, tool_name: "bash", tool_input: { command: "start" } },
		});
		assert.deepEqual(out.updatedInput, { command: "one-two" });
	});

	it("stops the chain once a hook blocks", async () => {
		const deny = await write("deny2.sh", `cat >/dev/null; echo no >&2; exit 2`);
		const after = await write("after.sh", `cat >/dev/null; jq -n '{hookSpecificOutput:{updatedInput:{command:"ran"}}}'`);
		const out = await runHooks({
			config: {
				PreToolUse: [
					{ matcher: "Bash", hooks: [{ type: "command", command: deny }] },
					{ matcher: "Bash", hooks: [{ type: "command", command: after }] },
				],
			},
			event: "PreToolUse",
			toolName: "bash",
			payload: { session_id: "t", cwd: dir, tool_name: "bash", tool_input: {} },
		});
		assert.ok(out.blocked);
		assert.equal(out.updatedInput, undefined, "a later hook ran after a block");
	});

	it("does nothing when no hook matches the tool", async () => {
		const path = await write("never.sh", `cat >/dev/null; echo ran`);
		const out = await runHooks({
			config: config(path),
			event: "PreToolUse",
			toolName: "read",
			payload: { session_id: "t", cwd: dir, tool_name: "read", tool_input: {} },
		});
		assert.deepEqual(out.context, []);
	});
});

describe("guardToolCall: hooks then gate", () => {
	let dir = "";

	const write = async (name: string, body: string) => {
		const path = join(dir, name);
		await writeFile(path, `#!/bin/bash\n${body}\n`);
		await chmod(path, 0o755);
		return path;
	};

	before(async () => {
		dir = await mkdtemp(join(tmpdir(), "harness-guard-"));
	});

	after(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	const guard = async (hookPath: string | undefined, denyMatching: string | undefined) => {
		const seen: string[] = [];
		const result = await guardToolCall({
			config: hookPath
				? { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: hookPath }] }] }
				: {},
			toolName: "bash",
			args: { command: "git status" },
			sessionId: "t",
			cwd: dir,
			primaryArgOf: (a) => a.command as string,
			check: async (req) => {
				seen.push(req.primaryArg ?? "");
				return denyMatching && req.primaryArg?.startsWith(denyMatching)
					? { reason: `denied: ${req.primaryArg}` }
					: undefined;
			},
		});
		return { result, seen };
	};

	it("shows the gate the REWRITTEN command, not the original", async () => {
		// The safety property. Gate-then-hooks would let any rewrite escape
		// every rule, because the gate would only ever see what the model
		// proposed rather than what is about to run.
		const hook = await write(
			"rw.sh",
			`cat >/dev/null; jq -n '{hookSpecificOutput:{updatedInput:{command:"rtk git status"}}}'`,
		);
		const { seen } = await guard(hook, undefined);
		assert.deepEqual(seen, ["rtk git status"]);
	});

	it("blocks when a deny rule matches only the rewritten form", async () => {
		const hook = await write(
			"rw2.sh",
			`cat >/dev/null; jq -n '{hookSpecificOutput:{updatedInput:{command:"rtk git status"}}}'`,
		);
		const { result } = await guard(hook, "rtk");
		assert.ok(result.blocked, "a rewrite escaped the deny rule");
	});

	it("does not consult the gate at all once a hook blocks", async () => {
		const hook = await write("deny3.sh", `cat >/dev/null; echo nope >&2; exit 2`);
		const { result, seen } = await guard(hook, undefined);
		assert.equal(result.blocked?.reason, "nope");
		assert.deepEqual(seen, [], "the gate ran after a hook block");
	});

	it("returns no args when nothing rewrote, so the call is untouched", async () => {
		const { result } = await guard(undefined, undefined);
		assert.equal(result.args, undefined);
		assert.equal(result.blocked, undefined);
	});
});
