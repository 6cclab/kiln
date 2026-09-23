import type { AgentHarnessTool } from "@earendil-works/pi-agent-core";
import type { TSchema } from "@earendil-works/pi-ai";
import type { ToolStrategy } from "../budget/tier.ts";
import type { McpTool } from "./client.ts";

/**
 * Tool gating — the whole point of the Phase 0 measurement.
 *
 * Measured on the real catalog (165 tools across 9 servers, 2026-09-22) against
 * a 32,768-token window:
 *
 *   full schemas, all 165      31,897 tokens   97% of the window
 *   index, all 165              7,597 tokens   23%
 *   index, coding posture       1,286 tokens    4%
 *
 * The plan assumed indexing would be nearly free. It is not — 23% of the window
 * is a lot. The 6x saving comes from Layer 0 simply not indexing servers that
 * are irrelevant to the task, which is why postures do more work here than the
 * dynamic search does.
 */

/**
 * Layer 0: static postures.
 *
 * Crude and free. A coding session has no business carrying UniFi or
 * occupational-therapy tools, and no runtime machinery is needed to know that.
 * Borrowed from Hermes' `toolsets.py`, which composes named tool groups the
 * same way.
 */
export interface Posture {
	name: string;
	description: string;
	/** Servers whose tools may be indexed at all. `"*"` means every server. */
	servers: string[];
}

export const POSTURES: Posture[] = [
	{
		name: "coding",
		description: "Code, deployments and observability. The default.",
		servers: ["infisical", "argocd-mcp", "personal-kb", "homelab-kb", "claude-relay", "sentry", "github"],
	},
	{
		name: "ops",
		description: "Infrastructure and monitoring.",
		servers: ["grafana", "proxmox", "argocd-mcp", "unifi-mcp", "pocket-id", "infisical"],
	},
	{
		name: "all",
		description: "Every configured server. Expensive on a small context window.",
		servers: ["*"],
	},
];

export function posture(name: string): Posture | undefined {
	return POSTURES.find((p) => p.name === name);
}

export function inPosture(tool: McpTool, active: Posture): boolean {
	return active.servers.includes("*") || active.servers.includes(tool.server);
}

/**
 * One `name: description` line per tool.
 *
 * Descriptions are first-line only: MCP descriptions routinely run to
 * paragraphs, and the index exists precisely to avoid paying for them.
 */
export function buildIndex(tools: readonly McpTool[]): string {
	return tools
		.map((t) => `${t.qualifiedName}: ${(t.description ?? "").split("\n")[0].slice(0, 160)}`)
		.join("\n");
}

/**
 * Relevance of a tool to a set of query terms.
 *
 * Flat substring matching scored every tool containing "dashboard" identically,
 * so a search for "list dashboards" returned `update_dashboard` and
 * `alerting_manage_silences` while missing `search_dashboards` — the tie was
 * broken by catalog order. Weighting name over description fixes that: a term
 * in the tool's own name is a far stronger signal than one buried in prose.
 */
function score(tool: McpTool, terms: string[]): number {
	const name = tool.name.toLowerCase();
	const description = (tool.description ?? "").toLowerCase();

	let total = 0;
	for (const term of terms) {
		// Singular/plural is the common case for these queries ("dashboards" vs
		// the tool's "dashboard"), and is not worth a stemmer.
		const stem = term.replace(/s$/, "");
		if (name === term || name === stem) total += 10;
		else if (name.includes(stem)) total += 4;
		if (description.includes(stem)) total += 1;
	}
	return total;
}

export interface GateState {
	/** Tools admitted for the rest of the session, by qualified name. */
	admitted: Set<string>;
}

/**
 * Layer 1: `tool_search`.
 *
 * Returns matching schemas and admits them for the session. The model is told
 * what became available, which is what `declareToolChanges` in pi's agent loop
 * announces automatically once `setActiveTools` changes.
 *
 * Scoring is deliberately simple substring matching over name and description.
 * A smarter ranker would be easy and is not the bottleneck: the expensive thing
 * was ever having all 165 schemas resident, not choosing between them.
 */
export function createToolSearch<TContext extends object | undefined>(args: {
	tools: readonly McpTool[];
	posture: Posture;
	state: GateState;
	/** Called after admitting tools so the caller can update active tools. */
	onAdmit: (names: string[]) => Promise<void>;
	maxResults?: number;
}): AgentHarnessTool<TContext> {
	const { tools, state, onAdmit } = args;
	const maxResults = args.maxResults ?? 5;
	const searchable = tools.filter((t) => inPosture(t, args.posture));

	return {
		name: "tool_search",
		label: "Search tools",
		description:
			`Search ${searchable.length} available MCP tools by keyword and enable the matches for this session. ` +
			`Use this when you need a capability you do not already have a tool for, such as querying Grafana, ` +
			`reading secrets, or managing deployments. Returns the tools' full schemas.`,
		parameters: {
			type: "object",
			properties: {
				query: { type: "string", description: "Keywords describing the capability you need." },
			},
			required: ["query"],
		} as unknown as TSchema,
		execute: async (_id: string, params: unknown) => {
			const query = String((params as { query?: string }).query ?? "").toLowerCase();
			const terms = query.split(/\s+/).filter(Boolean);

			const scored = searchable
				.map((tool) => ({ tool, score: score(tool, terms) }))
				.filter((s) => s.score > 0)
				// Break ties by name length: with equal relevance the shorter name
				// is the more general tool (`search_dashboards` over
				// `get_dashboard_property`), which is usually what was meant.
				.sort((a, b) => b.score - a.score || a.tool.name.length - b.tool.name.length)
				.slice(0, maxResults);

			if (scored.length === 0) {
				// Name the posture in the failure: "no such tool" and "that tool
				// exists but this posture excludes it" need different fixes.
				return {
					content: [
						{
							type: "text" as const,
							text:
								`No tools matched "${query}" in the "${args.posture.name}" posture ` +
								`(${searchable.length} searchable of ${tools.length} total). ` +
								`Switch posture with /posture if the capability belongs to another server.`,
						},
					],
					details: undefined,
				};
			}

			for (const { tool } of scored) state.admitted.add(tool.qualifiedName);
			await onAdmit(scored.map((s) => s.tool.qualifiedName));

			const payload = scored.map(({ tool }) => ({
				name: tool.qualifiedName,
				description: tool.description,
				parameters: tool.inputSchema,
			}));

			return {
				content: [
					{
						type: "text" as const,
						text: `Enabled ${scored.length} tool(s):\n\n${JSON.stringify(payload, null, 2)}`,
					},
				],
				details: undefined,
			};
		},
	} as unknown as AgentHarnessTool<TContext>;
}

/**
 * Which MCP tools should be active, given the tier's strategy.
 *
 * `full-schemas` skips gating entirely — on a 200k window the whole catalog is
 * ~16% and the round-trip through search costs more than it saves.
 */
export function activeToolNames(args: {
	tools: readonly McpTool[];
	posture: Posture;
	strategy: ToolStrategy;
	state: GateState;
	residentTools: string[];
}): string[] {
	const { tools, state, residentTools } = args;

	if (args.strategy === "full-schemas") {
		return [...residentTools, ...tools.filter((t) => inPosture(t, args.posture)).map((t) => t.qualifiedName)];
	}

	// posture-index and full-index both start gated; they differ in how much of
	// the catalog the index describes, which is handled by the index text.
	return [...residentTools, "tool_search", ...state.admitted];
}
