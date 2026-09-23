#!/usr/bin/env -S node --experimental-strip-types
/**
 * Phase 5 runtime acceptance.
 *
 * The static measurement proved the catalog *can* be cut to 5% of the window.
 * This proves the mechanism actually works in a live session: the model starts
 * without Grafana tools, calls `tool_search`, and the tools become active.
 *
 * Asserts on the active tool set rather than on what the model says, because a
 * model claiming it enabled something is not evidence that it did.
 */

import { BACKGROUND_CONTEXT } from "@earendil-works/pi-agent-core";
import { NodeExecutionEnv } from "@earendil-works/pi-agent-core/node";
import { createRegistry } from "../src/provider/registry.ts";
import { startSession } from "../src/agent/session.ts";
import { McpHub, readServerConfigs, toHarnessTool } from "../src/mcp/client.ts";
import { activeToolNames, buildIndex, createToolSearch, inPosture, posture } from "../src/mcp/gating.ts";

const providerId = process.argv[2] ?? "anthropic";
const modelId = process.argv[3] ?? "claude-sonnet-4-5";

const reg = createRegistry({ ollama: { url: process.env.OLLAMA_HOST, serverDefaultContext: 49152 } });
await reg.models.refresh({ providers: [providerId] });
const resolved = await reg.resolve(providerId, modelId);

const hub = new McpHub();
await hub.connectAll(await readServerConfigs());
const mcpTools = hub.getTools();
console.log(`${mcpTools.length} MCP tools across ${hub.getStatuses().filter((s) => s.ok).length} servers`);

// "ops" so Grafana is searchable; the point is that it is NOT resident.
const activePosture = posture("ops")!;
const gate = { admitted: new Set<string>() };
const RESIDENT = ["bash", "read", "edit", "write"];
const cwd = process.cwd();
const env = new NodeExecutionEnv({ cwd });

// Force gating regardless of tier, so this tests the mechanism rather than
// whichever strategy the chosen model's window happens to select.
const STRATEGY = "posture-index" as const;

let session: Awaited<ReturnType<typeof startSession>>;

const toolSearch = createToolSearch<{ env: typeof env }>({
	tools: mcpTools,
	posture: activePosture,
	state: gate,
	onAdmit: async () => {
		await session.lane.setActiveTools(
			activeToolNames({ tools: mcpTools, posture: activePosture, strategy: STRATEGY, state: gate, residentTools: RESIDENT }),
			BACKGROUND_CONTEXT,
		);
	},
});

const scoped = mcpTools.filter((t) => inPosture(t, activePosture));

session = await startSession({
	registry: reg,
	resolved,
	cwd,
	sessionsDir: "/tmp/harness-gating",
	extraTools: [...mcpTools.map((t) => toHarnessTool<{ env: typeof env }>(hub, t)), toolSearch] as never,
	activeToolNames: activeToolNames({
		tools: mcpTools,
		posture: activePosture,
		strategy: STRATEGY,
		state: gate,
		residentTools: RESIDENT,
	}),
	systemPrompt:
		"You are a coding assistant operating in a terminal.\n\n" +
		`Additional tools are available but not loaded. Call tool_search to enable any you need.\n\n<available_tools>\n${buildIndex(scoped)}\n</available_tools>`,
});

const before = await session.lane.getActiveTools(BACKGROUND_CONTEXT);
console.log(`\nactive at turn 1: ${before.length} tools -> ${before.join(", ")}`);

const grafanaResident = before.filter((t) => t.includes("grafana"));
console.log(`grafana tools resident at start: ${grafanaResident.length} (must be 0)`);

console.log("\nasking for something that needs Grafana...\n");
const result = await session.lane.prompt(
	"List the Grafana dashboards. Use tool_search first to find the right tool, then stop — do not actually query anything else.",
	undefined,
	BACKGROUND_CONTEXT,
);

const after = await session.lane.getActiveTools(BACKGROUND_CONTEXT);
const admitted = [...gate.admitted];

console.log(`status: ${result.ok ? ((result.value as { status?: string }).status ?? "?") : "error"}`);
console.log(`active after: ${after.length} tools`);
console.log(`admitted:     ${admitted.length ? admitted.join(", ") : "(none)"}`);

const pass = grafanaResident.length === 0 && admitted.length > 0 && after.length > before.length;
console.log(`\nRUNTIME GATE: ${pass ? "PASS" : "FAIL"}`);
console.log(
	pass
		? `  started with ${before.length} tools, ended with ${after.length}, out of ${mcpTools.length} registered`
		: "  gating did not admit tools on demand",
);

await hub.close();
process.exit(pass ? 0 : 1);
