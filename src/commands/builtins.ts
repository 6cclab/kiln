import { BACKGROUND_CONTEXT } from "@earendil-works/pi-agent-core";
import type { StartedSession } from "../agent/session.ts";
import type { Registry } from "../provider/registry.ts";
import type { Model } from "@earendil-works/pi-ai";
import { TOOL_STRATEGY_COST, tierFor, usableTokens } from "../budget/tier.ts";
import { formatTokens } from "../tui/transcript.ts";
import { staticSource, type Command, type CommandSource } from "./registry.ts";

/**
 * Built-in slash commands.
 *
 * Most are thin: `/compact`, `/model` and `/context` map onto `lane.compact()`,
 * `harness.setModel()` and tier data that already exist. The registry is the
 * work; these are bindings.
 *
 * Commands that are Anthropic-account or Anthropic-infrastructure specific are
 * omitted rather than stubbed — see docs/claude-code-parity.md. A command that
 * exists and does nothing is worse than one that is absent.
 */

export interface BuiltinDeps {
	session: StartedSession;
	registry: Registry;
	onClear: () => void;
	onExit: () => void;
	/** Subagent roster, for `/agents`. */
	agents?: readonly import("../claude/agents.ts").AgentDefinition[];
}

export function builtinCommands(deps: BuiltinDeps): CommandSource {
	const { session, registry } = deps;

	// Argument completions run on every keystroke, and `available()` can reach
	// the network. Cached for a few seconds: long enough that typing is free,
	// short enough that a model pulled mid-session shows up.
	let modelCache: { at: number; models: readonly Model<never>[] } | undefined;
	const cachedModels = async (): Promise<readonly Model<never>[]> => {
		if (modelCache && Date.now() - modelCache.at < 5_000) return modelCache.models;
		const models = await registry.available();
		modelCache = { at: Date.now(), models };
		return models;
	};

	const commands: Omit<Command, "origin">[] = [
		{
			name: "help",
			description: "Show available commands",
			run: () => ({ output: "" }), // Filled in by the registry; see wiring below.
		},
		{
			name: "clear",
			description: "Clear conversation history",
			run: () => {
				deps.onClear();
				return { output: "Conversation cleared." };
			},
		},
		{
			name: "compact",
			description: "Summarize and compact the current context",
			run: async () => {
				await session.lane.compact({}, BACKGROUND_CONTEXT);
				return { output: "Context compacted." };
			},
		},
		{
			name: "context",
			description: "Show context usage against the active tier",
			run: () => {
				const t = session.tier;
				return {
					output: [
						`window        ${t.contextWindow}`,
						`tier          ${t.name}`,
						`tools         ${t.toolStrategy} (${TOOL_STRATEGY_COST[t.toolStrategy]} tokens)`,
						`system prompt ${t.systemPromptTokens} max`,
						`reserved      ${t.compaction.reserveTokens}`,
						`available     ${usableTokens(t)} for conversation`,
					].join("\n"),
				};
			},
		},
		{
			name: "cost",
			description: "Show token usage and cost for this session",
			run: async () => {
				const model = await session.lane.getModel(BACKGROUND_CONTEXT);
				// Self-hosted models have zero marginal cost; saying "$0.00" is
				// more honest than omitting the line and implying it is unknown.
				const rate = model?.cost;
				return {
					output: rate
						? `input $${rate.input}/M · output $${rate.output}/M${rate.input === 0 ? "  (self-hosted, no marginal cost)" : ""}`
						: "No cost data for this model.",
				};
			},
		},
		{
			name: "model",
			description: "Show or change the active model",
			argumentHint: "<provider/model>",
			/**
			 * The picker. Typing `/model ` lists every model you can reach, with
			 * its window and tier, and selecting one fills it in.
			 *
			 * Without this the command required knowing and typing an exact
			 * `provider/model` id — which for a local Ollama host means
			 * remembering tag strings like `qwen3:30b-a3b`. The list is the
			 * feature; the command was only half of it.
			 *
			 * Results are cached: this runs on every keystroke after `/model `,
			 * and `available()` can touch the network.
			 */
			getArgumentCompletions: async (prefix: string) => {
				const models = await cachedModels();
				const wanted = prefix.trim().toLowerCase();
				return models
					.filter((m) => !wanted || `${m.provider}/${m.id}`.toLowerCase().includes(wanted))
					.map((m) => {
						const tier = tierFor(m);
						return {
							value: `${m.provider}/${m.id}`,
							label: `${m.provider}/${m.id}`,
							// What actually differs between them, and what decides
							// whether a task will fit.
							description: `${formatTokens(m.contextWindow)} · ${tier.name} · ${formatTokens(usableTokens(tier))} usable`,
						};
					});
			},
			run: async ({ args }) => {
				if (!args) {
					const current = await session.lane.getModel(BACKGROUND_CONTEXT);
					const available = await registry.available();
					return {
						output: [
							`current: ${current?.provider}/${current?.id}`,
							"",
							...available.map((m) => `  ${m.provider}/${m.id}`),
						].join("\n"),
					};
				}
				const [provider, ...rest] = args.split("/");
				const modelId = rest.join("/");
				if (!provider || !modelId) return { output: "Usage: /model <provider>/<model>" };

				const resolved = await registry.resolve(provider, modelId);
				await session.lane.setModel({ provider, modelId }, BACKGROUND_CONTEXT);
				return {
					output: `Model set to ${provider}/${modelId} (${resolved.tier.name} tier, ${formatTokens(usableTokens(resolved.tier))} budget).`,
				};
			},
		},
		{
			name: "tools",
			description: "Show which tools are currently resident",
			run: async () => {
				const active = await session.lane.getActiveTools(BACKGROUND_CONTEXT);
				return {
					output: [`${active.length} resident:`, ...active.map((t) => `  ${t}`)].join("\n"),
				};
			},
		},
		{
			name: "agents",
			description: "Show the subagents available for dispatch",
			run: async () => {
				const agents = deps.agents ?? [];
				if (agents.length === 0) return { output: "No subagents. Define them in .claude/agents/*.md" };
				return {
					output: [
						`${agents.length} available:`,
						...agents.map((a) => {
							// The model it will actually land on is not knowable
							// until dispatch (an alias may inherit), so the
							// request is shown rather than a guess at the result.
							const model = a.model ? ` (${a.model})` : "";
							const tools = a.tools ? ` [${a.tools.length} tools]` : "";
							return `  ${a.name}${model}${tools}\n    ${a.description}`;
						}),
					].join("\n"),
				};
			},
		},
		{
			name: "status",
			description: "Show model, provider and connection status",
			run: async () => {
				const model = await session.lane.getModel(BACKGROUND_CONTEXT);
				const check = model ? await registry.models.checkAuth(model.provider) : undefined;
				return {
					output: [
						`model     ${model?.provider}/${model?.id}`,
						`auth      ${check ? `${check.type} (${check.source ?? "configured"})` : "none"}`,
						`tier      ${session.tier.name}`,
						`sessions  ${session.sessionsDir}`,
					].join("\n"),
				};
			},
		},
		{
			name: "exit",
			description: "Exit the harness",
			run: () => {
				deps.onExit();
				return {};
			},
		},
	];

	return staticSource("builtin", commands);
}

/**
 * `/help` needs the registry that contains it, so it is bound after
 * construction rather than capturing a half-built reference.
 */
export function bindHelp(source: CommandSource, list: () => Promise<Command[]>): CommandSource {
	return {
		origin: source.origin,
		load: async () => {
			const commands = await source.load();
			return commands.map((cmd) =>
				cmd.name === "help"
					? {
							...cmd,
							run: async () => ({
								output: (await list())
									.map((c) => `  /${c.name}${c.argumentHint ? ` ${c.argumentHint}` : ""}  ${c.description ?? ""}`)
									.join("\n"),
							}),
						}
					: cmd,
			);
		},
	};
}
