import { join } from "node:path";
import { homedir } from "node:os";
import { AgentHarness, BACKGROUND_CONTEXT, JsonlSessionRepo } from "@earendil-works/pi-agent-core";
import { NodeExecutionEnv } from "@earendil-works/pi-agent-core/node";
import {
	createBashTool,
	createEditTool,
	createReadTool,
	createWriteTool,
} from "@earendil-works/pi-agent-core";
import type { AgentHarnessTool, AgentLane, Context, ExecutionEnv, Skill } from "@earendil-works/pi-agent-core";
import type { Message } from "@earendil-works/pi-ai";
import type { Registry, ResolvedModel } from "../provider/registry.ts";
import { applySuppression } from "../provider/reasoning.ts";
import type { Tier } from "../budget/tier.ts";

/**
 * The agent session: resident tools plus the loop, headless.
 *
 * Everything here is deliberately presentation-free. The TUI (Phase 3) consumes
 * the same `AgentEvent` stream this produces, so the loop never learns whether
 * anything is being drawn. That is what lets the UI be built after the engine
 * without the engine being reshaped around it.
 */

/** Filesystem + shell context the built-in execution tools require. */
export interface HarnessToolContext {
	env: ExecutionEnv;
}

/**
 * The resident tool set.
 *
 * Four tools today, always in context, against a measured MCP catalog of 165
 * tools / 31,897 tokens that is not. `skill` and `session_search` join in
 * Phases 4 and 6; everything else stays gated behind `tool_search` in Phase 5.
 * The set is kept small on purpose: the `small` tier budgets ~1.3k tokens of
 * tools in total.
 */
export function residentTools(): AgentHarnessTool<HarnessToolContext>[] {
	return [
		createBashTool<HarnessToolContext>(),
		createReadTool<HarnessToolContext>(),
		createEditTool<HarnessToolContext>(),
		createWriteTool<HarnessToolContext>(),
	] as AgentHarnessTool<HarnessToolContext>[];
}

export interface StartSessionOptions {
	registry: Registry;
	resolved: ResolvedModel;
	/** Working directory the tools operate in. Defaults to process.cwd(). */
	cwd?: string;
	/** Session store root. Defaults to ~/.harness/sessions. */
	sessionsDir?: string;
	systemPrompt?: string;
	/** Skills exposed to the model. Only name+description stay resident. */
	skills?: Skill[];
	/** MCP tools and tool_search. Registered but mostly inactive; see activeToolNames. */
	extraTools?: AgentHarnessTool<HarnessToolContext>[];
	/**
	 * Tools actually sent to the model. Everything else stays registered but
	 * dormant, which is the whole gating mechanism: registration is free,
	 * activation costs schema tokens.
	 */
	activeToolNames?: string[];
	/** Cancellation scope. Defaults to the un-cancelled root. */
	context?: Context;
	/** Resume this session id instead of creating one. */
	resumeId?: string;
	/** Resume the most recently modified session in this cwd. */
	resumeLatest?: boolean;
	/**
	 * Reasoning effort, from `--effort`.
	 *
	 * pi maps this onto whatever the provider actually supports - a token budget
	 * on one, an effort string on another - so the harness sets one field rather
	 * than branching per provider.
	 */
	thinkingLevel?: "low" | "medium" | "high" | "xhigh" | "max";
	/** Session name, from `--name`. Shown in the picker and the terminal title. */
	name?: string;
	/**
	 * Branch instead of continuing in place, from `--fork-session`.
	 *
	 * Without it, resuming appends to the original session: a speculative "what
	 * if I had asked X instead" overwrites the history it was exploring from.
	 * Forking copies the tree into a new session and leaves the source intact.
	 */
	fork?: boolean;
	/**
	 * Create the session with this id, from `--session-id`.
	 *
	 * Distinct from `resumeId`: that reopens an existing session and silently
	 * starts a fresh one if the id is unknown. A caller that passes an id it
	 * generated wants *that* id to exist afterwards, which is the whole point
	 * when a surrounding script is going to refer to it later.
	 */
	sessionId?: string;
}

const DEFAULT_SYSTEM_PROMPT = [
	"You are a coding assistant operating in a terminal.",
	"Use the provided tools to inspect and modify the user's code.",
	"Prefer reading a file before editing it. Keep responses short.",
].join(" ");

/** Lane the interactive conversation runs on. Sub-agents would take their own. */
export const MAIN_LANE = "main";

export interface StartedSession {
	harness: AgentHarness<HarnessToolContext>;
	/**
	 * The conversation lane: `prompt()`, `abort()`, `steer()`, and
	 * `setActiveTools()` live here, not on the harness. Phase 5's tool gating
	 * drives `setActiveTools`; Phase 3's Esc-to-interrupt drives `abort`.
	 */
	lane: AgentLane;
	tier: Tier;
	/** Session id, as hooks receive it in `session_id`. */
	sessionId: string;
	/**
	 * Absolute path of this session's JSONL log, passed to hooks as
	 * `transcript_path`. The SessionEnd hook on this machine reads the
	 * transcript from it, so an absent path silently disables that hook.
	 */
	transcriptPath: string;
	/** Directory the JSONL session log was written to. Phase 6 indexes this. */
	sessionsDir: string;
	/** Filesystem/shell env, reused by .claude loaders. */
	env: ExecutionEnv;
}

export async function startSession(opts: StartSessionOptions): Promise<StartedSession> {
	const cwd = opts.cwd ?? process.cwd();
	const sessionsDir = opts.sessionsDir ?? join(homedir(), ".harness", "sessions");
	const { model, tier, suppression } = opts.resolved;

	const env = new NodeExecutionEnv({ cwd });

	// `Context` is chord's Go-style context (cancellation + values), not a plain
	// options bag. BACKGROUND_CONTEXT is the un-cancelled root; callers that
	// need interruption derive one with `withAbortSignal`.
	const context: Context = opts.context ?? BACKGROUND_CONTEXT;

	// JSONL, matching Claude Code's own on-disk shape: append-only, one file per
	// session, greppable. Phase 6's FTS5 index reads these directly rather than
	// maintaining a second copy of the transcript.
	//
	// The repo takes the same `FileSystem` the tools use, so a future remote or
	// sandboxed env moves sessions with it for free.
	const repo = new JsonlSessionRepo({ fileSystem: env, sessionsRoot: sessionsDir });

	// Resume by opening existing metadata; fall back to a fresh session rather
	// than failing, so a stale id from a deleted session does not block startup.
	let session = undefined;
	if (opts.resumeId || opts.resumeLatest) {
		const candidates = await repo.list({ cwd }, context);
		const match = opts.resumeId
			? candidates.find((m) => m.id === opts.resumeId)
			: candidates.sort((a, b) => b.modifiedAt - a.modifiedAt)[0];
		if (match) {
			session = opts.fork
				? // `scope: "tree"` copies the whole conversation and every branch
					// tip, so the fork can be navigated exactly like the original.
					await repo.fork(match, { scope: "tree" }, context)
				: await repo.open(match, context);
		}
	}
	session ??= await repo.create(opts.sessionId ? { cwd, id: opts.sessionId } : { cwd }, context);

	const { harness } = await AgentHarness.create<HarnessToolContext>(
		{
			session,
			models: opts.registry.models,
			model,
			tools: [...residentTools(), ...(opts.extraTools ?? [])],
			...(opts.activeToolNames ? { activeToolNames: opts.activeToolNames } : {}),
			toolContext: { env },
			systemPrompt: opts.systemPrompt ?? DEFAULT_SYSTEM_PROMPT,
			...(opts.thinkingLevel ? { thinkingLevel: opts.thinkingLevel as never } : {}),

			// Skills are resources, not tools: pi keeps only name+description
			// resident and injects the body on invocation. Measured on the real
			// ~/.claude/skills: 841 resident tokens vs 8,316 if bodies were loaded.
			...(opts.skills?.length ? { resources: { skills: opts.skills } } : {}),

			// The tier is the single point where model choice becomes behavior.
			// Nothing below this line knows which provider it is talking to.
			compaction: tier.compaction,

			// Tools run one at a time. A local model at ~21 tok/s gains nothing
			// from parallel execution, and sequential output is far easier to
			// render as a readable transcript.
			toolExecution: "sequential",

			// Reasoning suppression is applied at the transport boundary rather
			// than baked into the stored conversation. The session log keeps the
			// user's actual words; `/no_think` never becomes part of the
			// transcript, only part of the request.
			toProviderMessages: (messages) =>
				applySuppression(messages as unknown as { role: string; content?: unknown }[], suppression) as Message[],
		},
		context,
	);

	if (opts.name) await harness.setName(opts.name, context);

	const lane = await harness.lane(MAIN_LANE, context);

	const metadata = (session as { metadata?: { id?: string; path?: string } }).metadata ?? {};

	return {
		harness,
		lane,
		tier,
		sessionId: metadata.id ?? "unknown",
		transcriptPath: metadata.path ?? "",
		sessionsDir,
		env,
	};
}
