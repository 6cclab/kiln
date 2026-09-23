import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { join } from "node:path";
import { homedir, tmpdir } from "node:os";
import { PermissionGate } from "../src/claude/permission.ts";
import { suppressionFor, applySuppression } from "../src/provider/reasoning.ts";
import { resolveContextWindow } from "../src/provider/ollama.ts";

const WORK = join(tmpdir(), "harness-test-workspace");

describe("PermissionGate workspace boundary", () => {
	const gate = () =>
		new PermissionGate({
			// Deliberately permissive: the boundary must hold regardless.
			permissions: { allow: ["read", "write", "bash"], deny: [], ask: [] },
			mode: "auto",
			roots: [WORK],
		});

	it("allows paths inside the workspace", async () => {
		assert.equal(await gate().check({ toolName: "read", primaryArg: join(WORK, "a.ts"), args: { path: join(WORK, "a.ts") } }), undefined);
	});

	it("blocks a path outside the workspace even when the rule allows the tool", async () => {
		// `allow: [Read]` means "reading is fine here", not "read anything on
		// this machine". NodeExecutionEnv does not sandbox absolute paths.
		const key = join(homedir(), ".ssh", "id_rsa");
		const blocked = await gate().check({ toolName: "read", primaryArg: key, args: { path: key } });
		assert.ok(blocked, "escape was not blocked");
	});

	it("blocks traversal out of the workspace via ..", async () => {
		const escape = join(WORK, "..", "escape.txt");
		assert.ok(await gate().check({ toolName: "read", primaryArg: escape, args: { path: escape } }));
	});

	it("allows an outside path once its directory is added", async () => {
		const g = gate();
		const outside = join(tmpdir(), "elsewhere", "f.txt");
		assert.ok(await g.check({ toolName: "read", primaryArg: outside, args: { path: outside } }));
		g.addRoot(join(tmpdir(), "elsewhere"));
		assert.equal(await g.check({ toolName: "read", primaryArg: outside, args: { path: outside } }), undefined);
	});

	it("refuses rather than proceeds when there is no way to ask", async () => {
		// An unattended run must not take an action policy said needs confirming.
		const g = new PermissionGate({ permissions: { allow: [], deny: [], ask: [] }, mode: "manual", roots: [WORK] });
		const blocked = await g.check({ toolName: "write", primaryArg: join(WORK, "a.ts"), args: {} });
		assert.ok(blocked);
	});

	it("remembers an allow-always choice for the rest of the session", async () => {
		const g = new PermissionGate({ permissions: { allow: [], deny: [], ask: [] }, mode: "manual", roots: [WORK] });
		let asked = 0;
		g.setPrompter(async () => {
			asked++;
			return { kind: "allow-always" };
		});
		const req = { toolName: "write", primaryArg: join(WORK, "a.ts"), args: {} };
		await g.check(req);
		await g.check(req);
		assert.equal(asked, 1, "asked twice for a remembered grant");
	});

	it("turns a denial into feedback for the model, not an error", async () => {
		const g = new PermissionGate({ permissions: { allow: [], deny: [], ask: [] }, mode: "manual", roots: [WORK] });
		g.setPrompter(async () => ({ kind: "deny", feedback: "use the staging bucket" }));
		const blocked = await g.check({ toolName: "write", primaryArg: join(WORK, "a.ts"), args: {} });
		assert.ok(blocked?.reason.includes("use the staging bucket"));
	});
});

describe("reasoning suppression", () => {
	it("applies the /no_think suffix only to Ollama models that need it", () => {
		assert.equal(suppressionFor({ id: "qwen3-cc:latest", provider: "ollama", reasoning: true }), "no_think_suffix");
		assert.equal(suppressionFor({ id: "Qwen3.5:9b", provider: "ollama", reasoning: true }), "none");
	});

	it("never applies it to hosted providers", () => {
		// "/no_think" is a Qwen prompt convention. On Claude it injects a stray
		// token into the user's message and suppresses nothing.
		assert.equal(suppressionFor({ id: "claude-sonnet-4-5", provider: "anthropic", reasoning: true }), "none");
		assert.equal(suppressionFor({ id: "gpt-5.3-codex", provider: "openai-codex", reasoning: true }), "none");
	});

	it("does nothing for non-reasoning models", () => {
		assert.equal(suppressionFor({ id: "llama3.1:8b", provider: "ollama", reasoning: false }), "none");
	});

	it("appends to the last user message, not the system prompt", () => {
		const out = applySuppression(
			[
				{ role: "system", content: "sys" },
				{ role: "user", content: "hello" },
			],
			"no_think_suffix",
		);
		assert.equal(out[0].content, "sys");
		assert.ok(String(out[1].content).endsWith("/no_think"));
	});

	it("does not double-append", () => {
		const once = applySuppression([{ role: "user", content: "hi" }], "no_think_suffix");
		const twice = applySuppression(once, "no_think_suffix");
		assert.equal(twice[0].content, once[0].content);
	});
});

describe("resolveContextWindow", () => {
	it("prefers the loaded instance's real window", () => {
		assert.equal(resolveContextWindow({ ps: { model: "m", context_length: 49_152 }, serverDefault: 8_192 }), 49_152);
	});

	it("falls back to a Modelfile num_ctx", () => {
		// qwen3-cc pins 32768 while reporting a 262144 training context.
		assert.equal(
			resolveContextWindow({ show: { parameters: "top_k 20\nnum_ctx                        32768\n" } }),
			32_768,
		);
	});

	it("never uses the training context from model_info", () => {
		// Trusting it would pick the `large` tier and load 32k of schemas into 32k.
		const window = resolveContextWindow({ show: { model_info: { "qwen3moe.context_length": 262_144 } } });
		assert.notEqual(window, 262_144);
	});

	it("is conservative when nothing is known", () => {
		// Ollama's own default of 4096 is unrunnable; guessing high truncates silently.
		assert.equal(resolveContextWindow({}), 8_192);
	});
});
