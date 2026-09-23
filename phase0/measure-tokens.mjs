// Phase 0(a)+(b): exact token cost of tool catalogs.
//
// Method: Ollama reports `prompt_eval_count` — the real token count after the
// model's own chat template renders the tools. That is ground truth for the
// tokenizer that actually matters, so no client-side estimator is used.
// Cost of a tool set = prompt_eval_count(with tools) - prompt_eval_count(bare).

import { readFileSync } from "node:fs";
import { join } from "node:path";

const HOST = process.env.OLLAMA_HOST ?? "http://127.0.0.1:11434";
const MODEL = process.env.HARNESS_MODEL ?? "qwen3-cc:latest";

async function promptTokens(tools, content = "hi") {
  const res = await fetch(`${HOST}/api/chat`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      model: MODEL,
      messages: [{ role: "user", content }],
      tools,
      stream: false,
      think: false,
      // Generate as little as possible; only the prompt side is being measured.
      options: { num_predict: 1 },
    }),
  });
  const body = await res.json();
  if (body.error) throw new Error(body.error);
  return body.prompt_eval_count;
}

// MCP advertises JSON Schema under `inputSchema`; Ollama/OpenAI want it as
// `function.parameters`.
const toOllama = (t) => ({
  type: "function",
  function: { name: t.name, description: t.description, parameters: t.inputSchema },
});

const servers = JSON.parse(
  readFileSync(join(import.meta.dirname, "out", "mcp-tools.json"), "utf8"),
).filter((s) => s.ok);

console.log(`model: ${MODEL}   host: ${HOST}\n`);

const bare = await promptTokens(undefined);
console.log(`baseline (no tools):            ${bare} tokens\n`);

console.log("per-server cost:");
const rows = [];
for (const s of servers) {
  const n = await promptTokens(s.tools.map(toOllama));
  const cost = n - bare;
  rows.push({ server: s.server, tools: s.tools.length, cost });
  console.log(
    `  ${s.server.padEnd(14)} ${String(s.tools.length).padStart(3)} tools  ` +
      `${String(cost).padStart(6)} tokens  (${Math.round(cost / s.tools.length)}/tool)`,
  );
}

const allTools = servers.flatMap((s) => s.tools).map(toOllama);
console.log(`\nfull catalog (${allTools.length} tools):`);
let full;
try {
  full = (await promptTokens(allTools)) - bare;
  console.log(`  ${full} tokens`);
} catch (err) {
  console.log(`  FAILED: ${err.message}`);
  full = rows.reduce((n, r) => n + r.cost, 0);
  console.log(`  sum of per-server costs: ~${full} tokens`);
}

// The gating alternative: one "name: description" line per tool instead of a
// full schema. This is the number that decides whether Layer 1 is worth it.
const indexLines = servers
  .flatMap((s) => s.tools.map((t) => `${t.name}: ${(t.description ?? "").split("\n")[0]}`))
  .join("\n");
const indexCost = (await promptTokens(undefined, indexLines)) - bare;

const WINDOW = 32768;
console.log(`\n--- verdict against a ${WINDOW} token window ---`);
console.log(`full schemas:  ${full} tokens  = ${((full / WINDOW) * 100).toFixed(0)}% of the window`);
console.log(
  `index only:    ${indexCost} tokens  = ${((indexCost / WINDOW) * 100).toFixed(0)}% of the window` +
    `   (${(full / indexCost).toFixed(1)}x cheaper)`,
);
