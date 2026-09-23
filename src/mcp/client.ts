import { readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import type { AgentHarnessTool } from "@earendil-works/pi-agent-core";
import type { TSchema } from "@earendil-works/pi-ai";

/**
 * MCP client hub.
 *
 * Promoted from the Phase 0 spike, which connected to 9 of 11 configured
 * servers over both stdio and Streamable HTTP using the official SDK with no
 * custom transport code. Config is read from `~/.claude.json` so existing
 * servers work unmodified.
 *
 * The two Phase 0 failures were environmental, not architectural: `proxmox`
 * has no `node_modules` installed, and `sentry` needs OAuth.
 */

const CONNECT_TIMEOUT_MS = 30_000;

export interface McpServerConfig {
	type?: string;
	command?: string;
	args?: string[];
	env?: Record<string, string>;
	url?: string;
	headers?: Record<string, string>;
}

export interface McpTool {
	server: string;
	name: string;
	/** Fully-qualified, matching Claude's `mcp__server__tool` convention. */
	qualifiedName: string;
	description: string;
	inputSchema: unknown;
}

export interface ServerStatus {
	name: string;
	ok: boolean;
	error?: string;
	toolCount: number;
	ms: number;
}

export async function readServerConfigs(path?: string): Promise<Record<string, McpServerConfig>> {
	const file = path ?? join(homedir(), ".claude.json");
	try {
		const cfg = JSON.parse(await readFile(file, "utf8")) as { mcpServers?: Record<string, McpServerConfig> };
		return cfg.mcpServers ?? {};
	} catch {
		return {};
	}
}

/**
 * `type` is absent on some real entries (proxmox, grafana), so the transport is
 * inferred from shape: a `url` means HTTP, otherwise a stdio child process.
 */
function makeTransport(cfg: McpServerConfig) {
	const type = cfg.type ?? (cfg.url ? "http" : "stdio");
	if (type === "http" || type === "sse") {
		return new StreamableHTTPClientTransport(new URL(cfg.url as string), {
			requestInit: { headers: cfg.headers ?? {} },
		});
	}
	return new StdioClientTransport({
		command: cfg.command as string,
		args: cfg.args ?? [],
		// Children shell out to uv/npx/tsx and need the real PATH.
		env: { ...(process.env as Record<string, string>), ...(cfg.env ?? {}) },
		stderr: "ignore",
	});
}

async function withTimeout<T>(promise: Promise<T>, ms: number, label: string): Promise<T> {
	let timer: NodeJS.Timeout | undefined;
	try {
		return await Promise.race([
			promise,
			new Promise<never>((_, reject) => {
				timer = setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms);
			}),
		]);
	} finally {
		clearTimeout(timer);
	}
}

export class McpHub {
	private clients = new Map<string, Client>();
	private tools: McpTool[] = [];
	private statuses: ServerStatus[] = [];

	getTools(): readonly McpTool[] {
		return this.tools;
	}

	getStatuses(): readonly ServerStatus[] {
		return this.statuses;
	}

	/**
	 * Connect to every configured server and collect their catalogs.
	 *
	 * Sequential rather than parallel: several of these spawn `uvx`/`npx` child
	 * processes, and a stampede of cold starts muddies both timing and failure
	 * messages. A server that fails is recorded and skipped — one broken entry
	 * must not deny the user every other server.
	 */
	async connectAll(configs: Record<string, McpServerConfig>): Promise<void> {
		for (const [name, cfg] of Object.entries(configs)) {
			const started = Date.now();
			const client = new Client({ name: "harness", version: "0.0.0" }, { capabilities: {} });
			try {
				await withTimeout(client.connect(makeTransport(cfg)), CONNECT_TIMEOUT_MS, `${name} connect`);
				const { tools } = await withTimeout(client.listTools(), CONNECT_TIMEOUT_MS, `${name} listTools`);

				this.clients.set(name, client);
				for (const tool of tools) {
					this.tools.push({
						server: name,
						name: tool.name,
						qualifiedName: `mcp__${name}__${tool.name}`,
						description: tool.description ?? "",
						inputSchema: tool.inputSchema,
					});
				}
				this.statuses.push({ name, ok: true, toolCount: tools.length, ms: Date.now() - started });
			} catch (err) {
				await client.close().catch(() => {});
				this.statuses.push({
					name,
					ok: false,
					error: (err as Error).message,
					toolCount: 0,
					ms: Date.now() - started,
				});
			}
		}
	}

	async call(qualifiedName: string, args: Record<string, unknown>): Promise<string> {
		const tool = this.tools.find((t) => t.qualifiedName === qualifiedName);
		if (!tool) throw new Error(`Unknown MCP tool: ${qualifiedName}`);
		const client = this.clients.get(tool.server);
		if (!client) throw new Error(`Server not connected: ${tool.server}`);

		const result = await client.callTool({ name: tool.name, arguments: args });
		const content = (result.content ?? []) as Array<{ type: string; text?: string }>;
		return content
			.filter((c) => c.type === "text")
			.map((c) => c.text ?? "")
			.join("\n");
	}

	async close(): Promise<void> {
		await Promise.all([...this.clients.values()].map((c) => c.close().catch(() => {})));
		this.clients.clear();
	}
}

/**
 * Adapt an MCP tool to pi's tool interface.
 *
 * MCP's `inputSchema` is JSON Schema and pi expects a typebox `TSchema`; those
 * are the same thing at runtime, so the cast is structural rather than a lie.
 */
export function toHarnessTool<TContext extends object | undefined>(
	hub: McpHub,
	tool: McpTool,
): AgentHarnessTool<TContext> {
	return {
		name: tool.qualifiedName,
		label: `${tool.server}: ${tool.name}`,
		description: tool.description,
		parameters: (tool.inputSchema ?? { type: "object", properties: {} }) as TSchema,
		execute: async (_toolCallId: string, params: unknown) => {
			const text = await hub.call(tool.qualifiedName, (params ?? {}) as Record<string, unknown>);
			return { content: [{ type: "text" as const, text }], details: undefined };
		},
	} as unknown as AgentHarnessTool<TContext>;
}
