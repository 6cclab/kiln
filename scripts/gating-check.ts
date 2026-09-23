#!/usr/bin/env -S node --experimental-strip-types
/**
 * Phase 5 acceptance: does gating actually cut the catalog to the measured size?
 *
 * Connects to the real MCP servers, then measures each strategy's true token
 * cost with Ollama's `prompt_eval_count` — the same method Phase 0 used, so the
 * numbers are directly comparable rather than merely similar.
 */

import { execFile as execFileCb } from "node:child_process";
import { promisify } from "node:util";
import { McpHub, readServerConfigs } from "../src/mcp/client.ts";

const execFile = promisify(execFileCb);
import { buildIndex, inPosture, posture, POSTURES } from "../src/mcp/gating.ts";
import { tierForWindow } from "../src/budget/tier.ts";

const HOST = (process.env.OLLAMA_HOST ?? "http://127.0.0.1:11434").replace(/\/+$/, "");
const MODEL = process.env.HARNESS_MODEL ?? "qwen3-cc:latest";

/**
 * Measure via curl rather than fetch.
 *
 * Prompt-eval of the full 165-schema catalog is ~32k tokens, and a Tesla P40
 * evaluates at ~155 tok/s - roughly 200s for that one call. Node's fetch
 * enforces undici's own `headersTimeout` independently of `AbortSignal.timeout`,
 * and it is not configurable without pulling in undici directly. curl has no
 * such hidden ceiling.
 */
async function promptTokens(body: unknown): Promise<number> {
	const { stdout } = await execFile(
		"curl",
		["-s", "--max-time", "900", `${HOST}/api/chat`, "-H", "content-type: application/json", "-d", JSON.stringify(body)],
		{ maxBuffer: 64 * 1024 * 1024 },
	);
	if (!stdout.trim()) throw new Error("empty response (model still loading, or no VRAM available)");
	const json = JSON.parse(stdout.replace(/[\x00-\x08\x0B\x0C\x0E-\x1F]/g, " "));
	if (json.error) throw new Error(json.error);
	return json.prompt_eval_count as number;
}

const base = { model: MODEL, stream: false, think: false, options: { num_predict: 1 } };
const ask = (content: string, tools?: unknown) =>
	promptTokens({ ...base, messages: [{ role: "user", content }], ...(tools ? { tools } : {}) });

const hub = new McpHub();
console.log("connecting to MCP servers...\n");
await hub.connectAll(await readServerConfigs());

for (const s of hub.getStatuses()) {
	console.log(`  ${s.ok ? "ok  " : "FAIL"} ${s.name.padEnd(14)} ${s.ok ? `${s.toolCount} tools` : s.error?.slice(0, 60)}`);
}

const all = hub.getTools();
const connected = hub.getStatuses().filter((s) => s.ok).length;
console.log(`\n${connected}/${hub.getStatuses().length} servers, ${all.length} tools total\n`);

const bare = await ask("hi");
const toOllama = (t: (typeof all)[number]) => ({
	type: "function",
	function: { name: t.qualifiedName, description: t.description, parameters: t.inputSchema },
});

const WINDOW = 32768;
const tier = tierForWindow(WINDOW);
console.log(`measuring against a ${WINDOW}-token window (tier: ${tier.name}, strategy: ${tier.toolStrategy})\n`);

const rows: Array<[string, number]> = [];

rows.push(["full schemas, all servers", (await ask("hi", all.map(toOllama))) - bare]);
rows.push(["index, all servers", (await ask(buildIndex(all))) - bare]);

for (const p of POSTURES) {
	const scoped = all.filter((t) => inPosture(t, p));
	if (scoped.length === 0 || p.name === "all") continue;
	rows.push([`index, "${p.name}" posture (${scoped.length} tools)`, (await ask(buildIndex(scoped))) - bare]);
}

console.log(`  ${"strategy".padEnd(42)} ${"tokens".padStart(8)}  ${"% window".padStart(9)}`);
for (const [label, tokens] of rows) {
	console.log(`  ${label.padEnd(42)} ${String(tokens).padStart(8)}  ${`${((tokens / WINDOW) * 100).toFixed(0)}%`.padStart(9)}`);
}

const full = rows[0][1];
// The cheapest strategy, not simply the last row measured - postures are listed
// in declaration order, so "last" was reporting whichever happened to be final.
const best = Math.min(...rows.slice(1).map((r) => r[1]));
console.log(`\nreduction: ${full} -> ${best} tokens (${(full / best).toFixed(1)}x cheaper)`);

// The gate: the default posture's index must leave most of the window free.
const coding = posture("coding");
const codingCost = rows.find((r) => r[0].includes("coding"))?.[1] ?? Number.POSITIVE_INFINITY;
const pass = coding !== undefined && codingCost < WINDOW * 0.1;
console.log(`\nPHASE 5 GATE (coding posture under 10% of window): ${pass ? "PASS" : "FAIL"}`);

await hub.close();
process.exit(pass ? 0 : 1);
