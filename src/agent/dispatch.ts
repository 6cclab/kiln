import { BACKGROUND_CONTEXT } from "@earendil-works/pi-agent-core";
import type { AgentHarnessTool, Skill } from "@earendil-works/pi-agent-core";
import type { Registry } from "../provider/registry.ts";
import { resolveAgentModel, type ModelChoice } from "../claude/agents.ts";
import { allowedToolNames, type Dispatcher, type DispatchRequest, type DispatchResult } from "./subagent.ts";
import { startSession, type HarnessToolContext } from "./session.ts";
import { primaryArgOf, type PermissionGate } from "../claude/permission.ts";

/**
 * Running a subagent.
 *
 * A subagent gets its own `startSession` rather than its own lane on the
 * parent's harness. The reason is narrow and decides the design: pi's system
 * prompt is set per *harness*, not per lane, and a subagent whose system prompt
 * is its parent's is not a subagent — it is the same agent with a different
 * first message, which loses the specialization the definition exists to
 * express.
 *
 * A separate session also means a separate JSONL log, which is what makes a
 * subagent's work searchable afterwards instead of vanishing into a tool
 * result the parent summarized.
 */

export interface DispatcherOptions {
	registry: Registry;
	/** The parent's model, inherited unless the definition overrides it. */
	parentModel: ModelChoice;
	cwd: string;
	sessionsDir?: string;
	/** MCP and other non-resident tools, so an allowlist naming them can work. */
	extraTools?: AgentHarnessTool<HarnessToolContext>[];
	skills?: Skill[];
	/**
	 * The parent's permission gate.
	 *
	 * Not optional in spirit: the gate is installed as a `before_tool` hook on
	 * a harness, and a subagent gets its OWN harness. Without this, dispatching
	 * is a way to run tools the user would have been asked about - "have a
	 * subagent delete it" would bypass every rule the parent obeys. The SAME
	 * gate instance is reused, not a copy, so an allow-always granted inside a
	 * subagent is remembered by the parent and vice versa.
	 */
	gate?: PermissionGate;
	/** Progress, for the TUI. Never shown to the model. */
	onEvent?: (event: SubagentEvent) => void;
}

export type SubagentEvent =
	| { kind: "start"; agent: string; description: string; modelId: string; inherited: boolean }
	| { kind: "tool"; agent: string; toolName: string }
	| { kind: "done"; agent: string; toolCalls: number; chars: number }
	| { kind: "error"; agent: string; message: string };

export function createDispatcher(opts: DispatcherOptions): Dispatcher {
	return async (req: DispatchRequest): Promise<DispatchResult> => {
		const candidates = (await opts.registry.available()).map((m) => ({ id: m.id, provider: m.provider }));
		const choice = resolveAgentModel(req.agent.model, opts.parentModel, candidates);
		const inherited = choice.modelId === opts.parentModel.modelId && req.agent.model !== undefined;

		const resolved = await opts.registry.resolve(choice.providerId, choice.modelId);

		// The allowlist is applied to what this harness actually has, not to
		// what the definition imagined. A rule naming a tool that is not
		// installed narrows nothing rather than failing.
		const available = [
			"bash",
			"read",
			"edit",
			"write",
			...(opts.extraTools ?? []).map((t) => (t as { name: string }).name),
		];
		const activeToolNames = allowedToolNames(req.agent.tools, available);

		opts.onEvent?.({
			kind: "start",
			agent: req.agent.name,
			description: req.description,
			modelId: choice.modelId,
			inherited,
		});

		const session = await startSession({
			registry: opts.registry,
			resolved,
			cwd: opts.cwd,
			sessionsDir: opts.sessionsDir,
			// The definition's body IS the system prompt. That is the whole
			// point of a subagent, and the reason for a separate harness.
			systemPrompt: req.agent.prompt,
			skills: opts.skills,
			extraTools: opts.extraTools,
			activeToolNames,
		});

		// Installed before the first prompt, so no tool call can slip through
		// between session creation and the hook being attached.
		if (opts.gate) {
			const gate = opts.gate;
			session.harness.hooks.on("before_tool", async (event) => {
				const args = (event.args ?? {}) as Record<string, unknown>;
				const blocked = await gate.check({
					toolName: event.toolName,
					primaryArg: primaryArgOf(args),
					args,
				});
				return blocked ? { block: { reason: blocked.reason } } : undefined;
			});
		}

		let toolCalls = 0;
		let text = "";

		session.harness.events.on("tool_start", (event) => {
			toolCalls++;
			opts.onEvent?.({
				kind: "tool",
				agent: req.agent.name,
				toolName: (event as { toolName: string }).toolName,
			});
		});

		session.harness.events.on("message_end", (event) => {
			const message = (event as { message?: { role?: string; content?: unknown[] } }).message;
			if (message?.role !== "assistant") return;
			const chunk = (message.content ?? [])
				.filter((b): b is { type: "text"; text: string } => (b as { type?: string })?.type === "text")
				.map((b) => b.text)
				.join("");
			// Replaced, not accumulated: the parent wants the subagent's final
			// report, not a transcript of every intermediate thought it had on
			// the way there. Accumulating would reintroduce exactly the context
			// cost the isolation exists to avoid.
			if (chunk.trim()) text = chunk;
		});

		try {
			const result = await session.lane.prompt(req.prompt, undefined, BACKGROUND_CONTEXT);
			if (!result.ok) {
				const message = JSON.stringify((result as { error?: unknown }).error);
				opts.onEvent?.({ kind: "error", agent: req.agent.name, message });
				// Returned rather than thrown: a partial answer plus the error
				// is more use to the parent than the error alone.
				return { text: text || `The subagent failed: ${message}`, modelId: choice.modelId, toolCalls };
			}
		} catch (err) {
			opts.onEvent?.({ kind: "error", agent: req.agent.name, message: (err as Error).message });
			throw err;
		}

		opts.onEvent?.({ kind: "done", agent: req.agent.name, toolCalls, chars: text.length });
		return { text, modelId: choice.modelId, toolCalls };
	};
}
