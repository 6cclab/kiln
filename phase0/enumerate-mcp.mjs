// Phase 0(b): connect to every MCP server in ~/.claude.json, list tools, dump
// their real JSON schemas to disk so they can be token-counted with the actual
// model tokenizer (see measure-tokens.mjs).
//
// Doubles as the Phase 4a spike: if this connects to all 11 servers, the MCP
// client is a solved problem.

import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const CONNECT_TIMEOUT_MS = 30_000;

function loadServers() {
  const cfg = JSON.parse(readFileSync(join(homedir(), ".claude.json"), "utf8"));
  return Object.entries(cfg.mcpServers ?? {});
}

// `type` is absent on some entries (proxmox, grafana), so infer from shape:
// a `url` means HTTP, otherwise it is a stdio child process.
function makeTransport(cfg) {
  const type = cfg.type ?? (cfg.url ? "http" : "stdio");
  if (type === "http" || type === "sse") {
    return new StreamableHTTPClientTransport(new URL(cfg.url), {
      requestInit: { headers: cfg.headers ?? {} },
    });
  }
  return new StdioClientTransport({
    command: cfg.command,
    args: cfg.args ?? [],
    // Child needs the real PATH; MCP servers here shell out to uv/npx/tsx.
    env: { ...process.env, ...(cfg.env ?? {}) },
    stderr: "ignore",
  });
}

async function withTimeout(promise, ms, label) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, rej) => {
        timer = setTimeout(() => rej(new Error(`${label} timed out after ${ms}ms`)), ms);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function enumerate(name, cfg) {
  const client = new Client({ name: "harness-phase0", version: "0.0.0" }, { capabilities: {} });
  const transport = makeTransport(cfg);
  try {
    await withTimeout(client.connect(transport), CONNECT_TIMEOUT_MS, `${name} connect`);
    const { tools } = await withTimeout(client.listTools(), CONNECT_TIMEOUT_MS, `${name} listTools`);
    return { name, ok: true, tools };
  } catch (err) {
    return { name, ok: false, error: err.message, tools: [] };
  } finally {
    await client.close().catch(() => {});
  }
}

const servers = loadServers();
console.log(`Connecting to ${servers.length} MCP servers...\n`);

// Sequential: several of these spawn child processes, and a stampede of uvx/npx
// cold starts muddies the timing and the failure messages.
const results = [];
for (const [name, cfg] of servers) {
  const t0 = Date.now();
  const r = await enumerate(name, cfg);
  r.ms = Date.now() - t0;
  results.push(r);
  const status = r.ok ? `${String(r.tools.length).padStart(3)} tools` : `FAILED: ${r.error}`;
  console.log(`  ${name.padEnd(14)} ${status.padEnd(45)} ${r.ms}ms`);
}

const okResults = results.filter((r) => r.ok);
const total = okResults.reduce((n, r) => n + r.tools.length, 0);

mkdirSync(new URL(".", import.meta.url).pathname + "out", { recursive: true });
const outPath = join(new URL(".", import.meta.url).pathname, "out", "mcp-tools.json");
writeFileSync(
  outPath,
  JSON.stringify(
    results.map((r) => ({
      server: r.name,
      ok: r.ok,
      error: r.error,
      tools: r.tools.map((t) => ({
        name: t.name,
        description: t.description ?? "",
        inputSchema: t.inputSchema,
      })),
    })),
    null,
    2,
  ),
);

console.log(`\n${okResults.length}/${servers.length} servers connected, ${total} tools total`);
console.log(`Raw schemas -> ${outPath}`);
