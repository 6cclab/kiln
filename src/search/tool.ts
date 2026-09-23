import type { AgentHarnessTool } from "@earendil-works/pi-agent-core";
import type { TSchema } from "@earendil-works/pi-ai";
import type { SqliteSessionSearch } from "./sqlite.ts";

/**
 * `session_search` — recall across past sessions.
 *
 * This is the harness's entire "learning" story for v1, chosen deliberately
 * over self-written skills and agent-authored memory: it needs no judgment
 * about what is worth remembering, because everything is already on disk. The
 * only question is retrieval.
 *
 * The hard constraint is cost. A recall tool that returns whole entries spends
 * more context than it saves, which is exactly the trap on a 32k window. Hits
 * are therefore capped and snippet-only, and the cap is enforced here rather
 * than trusted to the caller.
 */

const DEFAULT_LIMIT = 5;
const MAX_LIMIT = 10;

export function createSessionSearchTool<TContext extends object | undefined>(
	search: SqliteSessionSearch,
): AgentHarnessTool<TContext> {
	return {
		name: "session_search",
		label: "Search past sessions",
		description:
			"Search your past conversations for prior work on a topic. Use this when the user refers to " +
			"something done before, or when you suspect a problem has been solved already. Returns short " +
			"snippets with dates, not full transcripts.",
		parameters: {
			type: "object",
			properties: {
				query: { type: "string", description: "Keywords to search for." },
				limit: { type: "number", description: `Max results (default ${DEFAULT_LIMIT}, max ${MAX_LIMIT}).` },
			},
			required: ["query"],
		} as unknown as TSchema,
		execute: async (_id: string, params: unknown) => {
			const { query, limit } = (params ?? {}) as { query?: string; limit?: number };
			const text = String(query ?? "").trim();
			if (!text) {
				return { content: [{ type: "text" as const, text: "No query provided." }], details: undefined };
			}

			// Index before searching so work from earlier in this very session is
			// findable. Sync is incremental — only changed files are re-read — so
			// this stays cheap even with hundreds of sessions.
			await search.sync();

			const hits = await search.searchSessions({
				text,
				limit: Math.min(limit ?? DEFAULT_LIMIT, MAX_LIMIT),
			});

			if (hits.length === 0) {
				return {
					content: [{ type: "text" as const, text: `No past sessions mention "${text}".` }],
					details: undefined,
				};
			}

			const lines = hits.map((hit) => {
				const when = hit.top?.timestamp ? new Date(hit.top.timestamp).toISOString().slice(0, 10) : "unknown";
				// Collapse whitespace: transcripts contain code blocks whose newlines
				// would otherwise multiply the line count of every hit.
				const snippet = (hit.top?.snippet ?? "").replace(/\s+/g, " ").trim();
				return `[${when}] ${snippet}\n  session: ${hit.sessionId}`;
			});

			return {
				content: [{ type: "text" as const, text: `${hits.length} prior session(s):\n\n${lines.join("\n\n")}` }],
				details: undefined,
			};
		},
	} as unknown as AgentHarnessTool<TContext>;
}
