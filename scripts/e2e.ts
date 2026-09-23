#!/usr/bin/env -S node --experimental-strip-types
/**
 * Phase 2 acceptance: a real multi-step task, end to end, through the loop.
 *
 * Deliberately not a mock. The point is to prove the whole chain works against
 * a real model: registry -> tier -> suppression -> agent loop -> tools ->
 * filesystem. It runs in a scratch directory so a confused model cannot damage
 * anything, and it asserts on the resulting files rather than on what the model
 * claimed to do - a model that says "done" without writing is the exact failure
 * this is meant to catch.
 *
 * Usage: scripts/e2e.ts [provider] [model]
 */

import { mkdtemp, readFile, writeFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { BACKGROUND_CONTEXT } from "@earendil-works/pi-agent-core";
import { createRegistry } from "../src/provider/registry.ts";
import { startSession } from "../src/agent/session.ts";
import { usableTokens } from "../src/budget/tier.ts";

const provider = process.argv[2] ?? "ollama";
const modelId = process.argv[3] ?? "Qwen3.5:9b";

const registry = createRegistry({
	ollama: {
		url: process.env.OLLAMA_HOST,
		serverDefaultContext: process.env.OLLAMA_CONTEXT_LENGTH ? Number(process.env.OLLAMA_CONTEXT_LENGTH) : undefined,
	},
});
await registry.models.refresh({ providers: [provider] });

const resolved = await registry.resolve(provider, modelId);
console.log(`model:    ${provider}/${modelId}`);
console.log(`window:   ${resolved.model.contextWindow}  tier=${resolved.tier.name}  usable=${usableTokens(resolved.tier)}`);
console.log(`suppress: ${resolved.suppression}\n`);

// Scratch project with a deliberate bug for the model to find and fix.
const cwd = await mkdtemp(join(tmpdir(), "harness-e2e-"));
await mkdir(join(cwd, "src"), { recursive: true });
await writeFile(
	join(cwd, "src", "math.js"),
	["export function add(a, b) {", "  return a - b;", "}", "", "export function mul(a, b) {", "  return a * b;", "}", ""].join("\n"),
);
await writeFile(join(cwd, "README.md"), "# scratch\n");
console.log(`scratch:  ${cwd}\n`);

const { lane } = await startSession({ registry, resolved, cwd });

const task =
	"In src/math.js the add function is wrong - it subtracts instead of adding. " +
	"Read the file, fix only that bug, and leave everything else alone.";

console.log(`task:     ${task}\n`);
const started = Date.now();

const result = await lane.prompt(task, undefined, BACKGROUND_CONTEXT);

const elapsed = ((Date.now() - started) / 1000).toFixed(1);

// `prompt()` returns a Result union, not the transcript. Status tells us whether
// the run terminated cleanly; the transcript itself is read back from the
// session, which doubles as a check that persistence actually happened.
const status = result.ok
	? ((result.value as { status?: string }).status ?? "?")
	: `ERROR ${JSON.stringify((result as { error?: unknown }).error).slice(0, 120)}`;

const entries = await lane.findEntries(undefined, BACKGROUND_CONTEXT);
const toolCalls: string[] = [];
for (const entry of entries) {
	const message = (entry as { message?: { role?: string; content?: unknown[] } }).message;
	if (message?.role !== "assistant") continue;
	for (const block of message.content ?? []) {
		const b = block as { type?: string; name?: string };
		if (b.type === "toolCall" || b.type === "tool_use") toolCalls.push(b.name ?? "?");
	}
}

console.log(`\n--- ${elapsed}s, status=${status}, ${entries.length} session entries ---`);
console.log(`tool calls: ${toolCalls.length ? toolCalls.join(" -> ") : "NONE"}`);

// The assertion that matters: did the file actually change on disk?
const after = await readFile(join(cwd, "src", "math.js"), "utf8");
const fixed = /return\s+a\s*\+\s*b/.test(after);
const mulIntact = /return\s+a\s*\*\s*b/.test(after);

console.log(`\nadd() fixed:      ${fixed ? "YES" : "NO"}`);
console.log(`mul() untouched:  ${mulIntact ? "YES" : "NO"}`);

if (!fixed || !mulIntact) {
	console.log("\n--- resulting file ---");
	console.log(after);
}

const pass = fixed && mulIntact && toolCalls.length > 0;
console.log(`\nPHASE 2 ACCEPTANCE: ${pass ? "PASS" : "FAIL"}`);
process.exit(pass ? 0 : 1);
