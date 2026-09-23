import type { AgentHarnessTool } from "@earendil-works/pi-agent-core";
import type { TSchema } from "@earendil-works/pi-ai";
import type { AgentDefinition } from "../claude/agents.ts";
import type { Tier } from "../budget/tier.ts";

/**
 * Subagent dispatch — the `task` tool.
 *
 * A subagent is a fresh agent with its own context window, its own system
 * prompt, and usually a narrower tool set. The parent sends a task and receives
 * only the final answer.
 *
 * ## Why this matters more here than in Claude Code
 *
 * The isolation is the feature. A subagent that reads thirty files to answer
 * one question spends thirty files' worth of tokens in *its* window; the parent
 * pays for one paragraph. On a 200k model that is a nicety. On a 32k model it
 * is the only way a search-heavy task completes at all — the parent would hit
 * compaction before it finished looking.
 *
 * So dispatch is not a convenience wrapper. It is a second context budget,
 * bought at the cost of one round-trip.
 *
 * ## What it costs to offer
 *
 * The catalog of agents is resident: the model cannot dispatch to an agent it
 * cannot see. That is the same trade as the MCP tool catalog, and it gets the
 * same treatment — the listing is budgeted against the tier rather than dumped
 * in full. See `describeAgents`.
 */

/** A built-in agent, so `task` is useful in a project with no definitions. */
export const GENERAL_PURPOSE: AgentDefinition = {
	name: "general-purpose",
	description:
		"Researches a question or searches the codebase across many files and returns only the conclusion. " +
		"Use when answering would mean reading more than a couple of files.",
	prompt: [
		"You are a research subagent. You have your own context window; the agent that dispatched you does not see",
		"your tool calls or intermediate reasoning, only your final message.",
		"",
		"Answer the task thoroughly, then report. Your final message IS the deliverable: state the conclusion and the",
		"evidence for it, with file paths and line numbers where they apply. Do not describe what you did.",
		"",
		"Do not ask follow-up questions - there is nobody to answer them. If the task is ambiguous, state the",
		"assumption you made and answer under it.",
	].join("\n"),
	source: "personal",
	path: "<built-in>",
};

export interface DispatchRequest {
	agent: AgentDefinition;
	prompt: string;
	description: string;
}

export interface DispatchResult {
	text: string;
	/** What actually ran, which may differ from what the definition asked for. */
	modelId?: string;
	/** Reported to the user, never to the model. */
	toolCalls?: number;
}

export type Dispatcher = (req: DispatchRequest) => Promise<DispatchResult>;

/**
 * Render the agent catalog for the tool description.
 *
 * Budgeted, for the same reason the MCP catalog is: this text is resident on
 * every turn. On the `small` tier a full listing of thirteen agents with
 * two-line descriptions would cost more than the entire tool budget, so the
 * descriptions are clipped there and dropped entirely if the roster is large.
 * A name the model can dispatch beats a description it cannot afford.
 */
export function describeAgents(agents: readonly AgentDefinition[], tier: Tier): string {
	if (agents.length === 0) return "";
	// Derived from the tier rather than hard-coded per tier name: a new tier
	// added later gets sensible behavior without editing this function.
	const perAgent = tier.name === "small" ? 120 : tier.name === "medium" ? 300 : Number.POSITIVE_INFINITY;

	const lines = agents.map((a) => {
		if (!Number.isFinite(perAgent)) return `- ${a.name}: ${a.description}`;
		const clipped =
			a.description.length > perAgent ? `${a.description.slice(0, perAgent).trimEnd()}...` : a.description;
		return `- ${a.name}: ${clipped}`;
	});
	return lines.join("\n");
}

export interface TaskToolOptions {
	agents: readonly AgentDefinition[];
	dispatch: Dispatcher;
	tier: Tier;
}

export function createTaskTool<TContext extends object | undefined>(
	opts: TaskToolOptions,
): AgentHarnessTool<TContext> {
	const names = opts.agents.map((a) => a.name);
	const catalog = describeAgents(opts.agents, opts.tier);

	return {
		name: "task",
		label: "Task",
		description: [
			"Dispatch a task to a subagent with its own context window. The subagent's tool calls and reasoning",
			"do not enter your context - you receive only its final report.",
			"",
			"Use it when answering would mean reading across many files, or for independent work that can run",
			"without your supervision. For a single lookup where you already know the file, read it yourself:",
			"dispatch costs a full round-trip.",
			"",
			"The subagent cannot ask you questions and does not see this conversation. Put everything it needs in",
			"the prompt.",
			"",
			"Available agents:",
			catalog,
		].join("\n"),
		parameters: {
			type: "object",
			properties: {
				subagent_type: {
					type: "string",
					// Enumerated rather than free-text: a dispatch to a name that
					// does not exist wastes a turn discovering it.
					enum: names,
					description: "Which agent to dispatch to.",
				},
				description: {
					type: "string",
					description: "A 3-5 word label for this task, shown to the user.",
				},
				prompt: {
					type: "string",
					description: "The task. Self-contained: the subagent sees none of this conversation.",
				},
			},
			required: ["subagent_type", "prompt"],
		} as unknown as TSchema,
		execute: async (_id: string, params: unknown) => {
			const args = (params ?? {}) as { subagent_type?: string; prompt?: string; description?: string };
			const wanted = String(args.subagent_type ?? "").trim();
			const prompt = String(args.prompt ?? "").trim();

			const agent = opts.agents.find((a) => a.name === wanted);
			if (!agent) {
				// Naming what does exist turns a dead turn into a usable one.
				return {
					content: [
						{
							type: "text" as const,
							text: `No agent named "${wanted}". Available: ${names.join(", ") || "none"}.`,
						},
					],
					details: undefined,
				};
			}
			if (!prompt) {
				return {
					content: [{ type: "text" as const, text: "No prompt given. Call again with the task." }],
					details: undefined,
				};
			}

			try {
				const result = await opts.dispatch({
					agent,
					prompt,
					description: String(args.description ?? "").trim() || agent.name,
				});
				return {
					content: [{ type: "text" as const, text: result.text || "(the subagent returned nothing)" }],
					details: undefined,
				};
			} catch (err) {
				// A failed subagent is a failed tool call, not a failed session.
				// The parent can retry, dispatch elsewhere, or do the work itself.
				return {
					content: [{ type: "text" as const, text: `Subagent "${agent.name}" failed: ${(err as Error).message}` }],
					details: undefined,
				};
			}
		},
	} as unknown as AgentHarnessTool<TContext>;
}

/**
 * Narrow the parent's tools to an agent's allowlist.
 *
 * Names are matched case-insensitively because the definitions are written in
 * Claude Code's casing (`Read`, `Grep`) while pi's tools are lowercase — the
 * same mismatch the permission rules had to handle.
 *
 * `task` is never included. Recursive dispatch turns one runaway agent into a
 * fork bomb against a paid API, and nothing in the subagent's job needs it.
 */
export function allowedToolNames(
	requested: readonly string[] | undefined,
	available: readonly string[],
): string[] {
	const usable = available.filter((n) => n !== "task");
	if (!requested) return usable;

	const wanted = new Set(requested.map((r) => r.toLowerCase()));
	const matched = usable.filter((n) => wanted.has(n.toLowerCase()));
	// An allowlist that matches nothing is far more likely to be a casing or
	// naming mismatch than a genuine request for a tool-less agent, and a
	// tool-less agent cannot do research at all.
	return matched.length > 0 ? matched : usable;
}
