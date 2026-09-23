#!/usr/bin/env -S node --experimental-strip-types --no-warnings
/**
 * Entry point.
 *
 * `env -S` splits the argument string so the node flag survives the shebang.
 * Node still needs --experimental-strip-types on v24 to run .ts directly.
 *
 *   harness                        start an interactive session
 *   harness models                 list runnable models with their tiers
 *   harness providers              list providers and auth status
 *   harness login <provider>       run a provider's login flow
 *   harness logout <provider>
 *
 * The interactive path is lazily imported so the management subcommands do not
 * pay for loading the TUI and agent stack.
 */

import { createRegistry, subscriptionProviders } from "./provider/registry.ts";
import { createTerminalAuthInteraction } from "./auth/interaction.ts";
import { usableTokens } from "./budget/tier.ts";
import { ContextTooSmallError } from "./budget/tier.ts";

const reg = createRegistry({
	ollama: {
		url: process.env.OLLAMA_HOST,
		// Ollama does not expose OLLAMA_CONTEXT_LENGTH over the API, so the
		// operator has to tell us; without it, models that pin no num_ctx fall
		// back to a conservative 8192.
		serverDefaultContext: process.env.OLLAMA_CONTEXT_LENGTH
			? Number(process.env.OLLAMA_CONTEXT_LENGTH)
			: undefined,
	},
});

import { parseArgs, HELP } from "./cli-args.ts";

const argv = parseArgs(process.argv.slice(2));
const command = argv.command;
const arg = argv.positional[0];

/** Read a piped prompt. Lets `git diff | harness -p "review this"` work. */
async function readStdin(): Promise<string> {
	const chunks: Buffer[] = [];
	for await (const chunk of process.stdin) chunks.push(chunk as Buffer);
	return Buffer.concat(chunks).toString("utf8");
}

async function listProviders(): Promise<void> {
	const subs = new Set(subscriptionProviders(reg.models).map((p) => p.id));
	const rows: string[] = [];

	for (const p of reg.models.getProviders()) {
		const check = await reg.models.checkAuth(p.id).catch(() => undefined);
		const kind = subs.has(p.id) ? "subscription" : p.auth?.oauth ? "oauth" : "api key";
		rows.push(
			`  ${p.id.padEnd(24)} ${kind.padEnd(13)} ${check ? `configured (${check.source ?? check.type})` : "-"}`,
		);
	}
	console.log(`${reg.models.getProviders().length} providers\n`);
	console.log(`  ${"provider".padEnd(24)} ${"auth".padEnd(13)} status`);
	console.log(rows.join("\n"));
	console.log(`\nLog in with a plan:  harness login <${[...subs].join("|")}>`);
}

async function listModels(): Promise<void> {
	// Dynamic providers only know their catalog after a refresh.
	await reg.models.refresh();

	const available = await reg.available();
	if (available.length === 0) {
		console.log("No models available. Configure a provider first: harness providers");
		return;
	}

	console.log(`${available.length} models available\n`);
	console.log(
		`  ${"provider".padEnd(16)} ${"model".padEnd(34)} ${"ctx".padStart(9)} ${"tier".padStart(7)} ${"tools".padStart(14)} ${"for convo".padStart(10)}`,
	);

	for (const m of [...available].sort((a, b) => `${a.provider}${a.id}`.localeCompare(`${b.provider}${b.id}`))) {
		try {
			const r = await reg.resolve(m.provider, m.id);
			console.log(
				`  ${m.provider.padEnd(16)} ${m.id.slice(0, 34).padEnd(34)} ${String(r.model.contextWindow).padStart(9)} ` +
					`${r.tier.name.padStart(7)} ${r.tier.toolStrategy.padStart(14)} ${String(usableTokens(r.tier)).padStart(10)}` +
					`${r.suppression === "none" ? "" : `  [${r.suppression}]`}`,
			);
		} catch (err) {
			// A model whose window cannot fit the harness floor is listed as
			// unusable rather than hidden - hiding it invites "why is my model
			// missing?" with no answer.
			const why = err instanceof ContextTooSmallError ? `window too small (short ${err.shortfall})` : "unavailable";
			console.log(`  ${m.provider.padEnd(16)} ${m.id.slice(0, 34).padEnd(34)} ${"-".padStart(9)} ${why}`);
		}
	}
}

async function login(providerId: string): Promise<void> {
	const provider = reg.models.getProvider(providerId);
	if (!provider) {
		console.error(`Unknown provider "${providerId}". See: harness providers`);
		process.exitCode = 1;
		return;
	}

	// Prefer the subscription/OAuth flow when the provider has one; an API key
	// is the fallback, not the default, since a plan is what most people have.
	const oauth = provider.auth?.oauth;
	const type = oauth ? "oauth" : "api_key";
	const label = oauth?.name ?? provider.auth?.apiKey?.name ?? providerId;
	console.log(`Logging in to ${label}...`);

	const controller = new AbortController();
	const onSigint = () => controller.abort();
	process.once("SIGINT", onSigint);
	try {
		await reg.login(providerId, type, createTerminalAuthInteraction({ signal: controller.signal }));
		console.log(`\nLogged in to ${providerId}.`);
	} catch (err) {
		console.error(`\nLogin failed: ${(err as Error).message}`);
		process.exitCode = 1;
	} finally {
		process.off("SIGINT", onSigint);
	}
}

/** Interactive session: the default when no subcommand is given. */
async function chat(): Promise<void> {
	const { startSession } = await import("./agent/session.ts");
	const { CommandRegistry } = await import("./commands/registry.ts");
	const { builtinCommands, bindHelp } = await import("./commands/builtins.ts");
	const { runApp } = await import("./tui/app.ts");
	const { BACKGROUND_CONTEXT } = await import("@earendil-works/pi-agent-core");

	const { loadMemory } = await import("./claude/memory.ts");
	const { loadClaudeSkills, skillCommandSource } = await import("./claude/skills.ts");
	const { claudeCommandSources } = await import("./claude/commands.ts");
	const { loadSettings } = await import("./claude/settings.ts");
	const { NodeExecutionEnv } = await import("@earendil-works/pi-agent-core/node");

	const cwd = process.cwd();
	// Settings are read before the model is chosen, because `model` may come
	// from them.
	const settings = await loadSettings(cwd, {
		sources: argv.settingSources as never,
		extra: argv.settings,
	});

	// `provider/model`, or a bare model id resolved against whatever is available.
	// qwen3.8 is pinned resident on the GPU on purpose, so it is the default: it
	// needs no load wait, and choosing anything larger would contend for VRAM.
	// Precedence: explicit flag, then settings.json, then the env var, then the
	// default. The flag wins because it is the most specific statement of intent.
	//
	// `settings.model` is only honored in `provider/model` form. The same file is
	// read by Claude Code, where `model` holds an alias like "opus[1m]" that
	// names nothing on this machine — taking it literally made every session
	// fail to start with `Unknown model "" on provider "opus[1m]"`.
	const fromSettings = settings.model?.includes("/") ? settings.model : undefined;
	const wanted = argv.model ?? fromSettings ?? process.env.HARNESS_MODEL ?? "ollama/qwen3.8:latest";
	const [provider, ...rest] = wanted.split("/");
	const modelId = rest.join("/");

	await reg.models.refresh({ providers: [provider] });
	const resolved = await reg.resolve(provider, modelId);

	// Memory is budgeted against the tier: on a 32k model the system prompt is
	// capped at ~2k tokens, and an unbounded CLAUDE.md would eat the session.
	const memory = await loadMemory(cwd, resolved.tier.systemPromptTokens);

	// --add-dir may be repeated, matching Claude Code's flag.
	const addDirs = argv.addDir.reduce<string[]>((acc, arg) => {
		if (arg) acc.push(arg);
		return acc;
	}, []);

	const env = new NodeExecutionEnv({ cwd });
	const skills = await loadClaudeSkills(env, cwd);

	// --- MCP -------------------------------------------------------------
	const { McpHub, readServerConfigs, toHarnessTool } = await import("./mcp/client.ts");
	const { activeToolNames, buildIndex, createToolSearch, inPosture, posture, POSTURES } = await import(
		"./mcp/gating.ts"
	);

	// Session search indexes BOTH the harness's own JSONL sessions and any
	// existing Claude Code history, so prior work is recallable from day one
	// rather than only after the harness has accumulated its own.
	const { SqliteSessionSearch } = await import("./search/sqlite.ts");
	const { createSessionSearchTool } = await import("./search/tool.ts");
	const sessionSearch = await SqliteSessionSearch.create();

	const permissionModeFlag = argv.permissionMode;

	// Permission gate: constructed before the session because the before_tool
	// hook needs it. Its prompter is bound later, once the TUI exists.
	const { PermissionGate, primaryArgOf } = await import("./claude/permission.ts");
	const permissionGate = new PermissionGate({
		permissions: {
			...settings.permissions,
			// Flags are additive to the settings files rather than replacing them:
			// `--allowed-tools` is "also allow this for this run", not "forget my
			// configuration". Deny still wins over both.
			allow: [...settings.permissions.allow, ...argv.allowedTools],
			deny: [...settings.permissions.deny, ...argv.disallowedTools],
		},
		// `--permission-mode <mode>`, as Claude Code documents it. The env var
		// stays as the lower-precedence form for a shell that sets it once.
		mode: (permissionModeFlag ?? process.env.HARNESS_PERMISSION_MODE ?? settings.permissions.defaultMode) as never,
		// cwd plus any --add-dir given at startup.
		roots: [cwd, ...addDirs],
	});

	const { createExitPlanModeTool, PLAN_MODE_PROMPT } = await import("./agent/plan-mode.ts");
	const planMode = {
		isActive: () => permissionGate.mode === "plan",
		onApprove: (mode: typeof permissionGate.mode) => permissionGate.setMode(mode),
	};
	// Bound to the TUI below; in print mode there is nobody to approve, so a plan
	// is reported rather than silently auto-approved.
	let approvePlan: (plan: string) => Promise<{ kind: "approve"; mode: typeof permissionGate.mode } | { kind: "revise"; feedback: string }> =
		async () => ({ kind: "revise", feedback: "No interactive approval available. Describe the plan in your reply instead." });

	const { BackgroundShells, createBackgroundBashTool, createBashOutputTool, createKillShellTool } = await import(
		"./agent/background-shell.ts"
	);
	const shells = new BackgroundShells();

	const { TodoStore, createTodoTool } = await import("./agent/todo.ts");
	const todos = new TodoStore();

	const hub = new McpHub();
	// `--strict-mcp-config` means use ONLY the given file, so the default is not
	// read at all rather than merged under it.
	await hub.connectAll(
		argv.strictMcpConfig && !argv.mcpConfig ? {} : await readServerConfigs(argv.mcpConfig),
	);
	for (const s of hub.getStatuses()) {
		if (!s.ok) console.warn(`mcp: ${s.name} unavailable — ${s.error?.slice(0, 100)}`);
	}

	let activePosture =
		posture(process.env.HARNESS_POSTURE ?? "coding") ?? (POSTURES[0] as NonNullable<ReturnType<typeof posture>>);
	const gate = { admitted: new Set<string>() };

	// session_search is resident, not gated: recall is cheap (~108 tokens for
	// three hits) and useless if the model has to discover it first.
	// Subagent definitions from .claude/agents. Loaded before the tool list so
	// `task` is only offered when there is something to dispatch to - an empty
	// roster would cost resident tokens to advertise nothing.
	const { loadAgents } = await import("./claude/agents.ts");
	const { createTaskTool, GENERAL_PURPOSE } = await import("./agent/subagent.ts");
	const { createDispatcher } = await import("./agent/dispatch.ts");
	const agents = [GENERAL_PURPOSE, ...(await loadAgents(cwd))];

	// Assigned after the TUI exists. A subagent runs for minutes with nothing on
	// screen otherwise, which is indistinguishable from a hang.
	let onSubagentEvent: ((e: import("./agent/dispatch.ts").SubagentEvent) => void) | undefined;

	const RESIDENT = [
		"bash",
		"read",
		"edit",
		"write",
		"session_search",
		"todo_write",
		"exit_plan_mode",
		"task",
		"bash_background",
		"bash_output",
		"kill_shell",
	];
	const mcpTools = hub.getTools();

	// Every MCP tool is registered; only a few are ever active. Registration is
	// free, activation is what costs schema tokens.
	const toolSearch = createToolSearch<{ env: typeof env }>({
		tools: mcpTools,
		posture: activePosture,
		state: gate,
		onAdmit: async (names) => {
			// Announced to the model by pi's declareToolChanges once the active
			// set changes, so no extra plumbing is needed to tell it what appeared.
			await session.lane.setActiveTools(
				activeToolNames({
					tools: mcpTools,
					posture: activePosture,
					strategy: resolved.tier.toolStrategy,
					state: gate,
					residentTools: RESIDENT,
				}),
				BACKGROUND_CONTEXT,
			);
			void names;
		},
	});

	const scoped = mcpTools.filter((t) => inPosture(t, activePosture));
	const index = buildIndex(scoped);

	// `--resume <id>` / `-c` (continue most recent), mirroring Claude Code's flags.

	const session = await startSession({
		registry: reg,
		resolved,
		cwd,
		skills,
		resumeId: typeof argv.resume === "string" ? argv.resume : undefined,
		sessionId: argv.sessionId,
		resumeLatest: argv.continueLatest === true || argv.resume === true,
		thinkingLevel: argv.effort,
		fork: argv.forkSession === true,
		name: argv.name,
		extraTools: [
			...mcpTools.map((t) => toHarnessTool<{ env: typeof env }>(hub, t)),
			toolSearch,
			createSessionSearchTool<{ env: typeof env }>(sessionSearch),
			createTodoTool<{ env: typeof env }>(todos),
			createExitPlanModeTool<{ env: typeof env }>({ controller: planMode, approve: (p) => approvePlan(p) }),
			createBackgroundBashTool<{ env: typeof env }>(shells),
			createBashOutputTool<{ env: typeof env }>(shells),
			createKillShellTool<{ env: typeof env }>(shells),
			createTaskTool<{ env: typeof env }>({
				agents,
				tier: resolved.tier,
				// Built lazily so the dispatcher closes over the MCP tools and
				// skills the parent ended up with, not a copy taken too early.
				dispatch: (req) =>
					createDispatcher({
						registry: reg,
						parentModel: { providerId: resolved.model.provider, modelId: resolved.model.id },
						cwd,
						skills,
						extraTools: mcpTools.map((t) => toHarnessTool<{ env: typeof env }>(hub, t)) as never,
						// The same gate, so a subagent cannot do what the parent
						// would have had to ask about.
						gate: permissionGate,
						onEvent: (e) => onSubagentEvent?.(e),
					})(req),
			}),
		] as never,
		activeToolNames: activeToolNames({
			tools: mcpTools,
			posture: activePosture,
			strategy: resolved.tier.toolStrategy,
			state: gate,
			residentTools: RESIDENT,
		}),
		systemPrompt: [
			// `--system-prompt` REPLACES the base instruction; the rest of the
			// assembly (plan mode, memory, the tool index) still applies, because
			// those describe the environment rather than the persona and dropping
			// them would leave the model unable to use its own tools.
			argv.systemPrompt ?? "You are a coding assistant operating in a terminal.",
			argv.appendSystemPrompt ?? "",
			permissionGate.mode === "plan" ? PLAN_MODE_PROMPT : "",
			memory.text,
			// The index is the point: one line per tool instead of a schema.
			// Measured at 1,514 tokens for the coding posture versus 39,386 for
			// the full catalog, which does not fit a 32k window at all.
			scoped.length
				? `Additional tools are available but not loaded. Call tool_search to enable any you need.\n\n<available_tools>\n${index}\n</available_tools>`
				: "",
		]
			.filter(Boolean)
			.join("\n\n"),
	});

	// Hooks from .claude/settings.json, accumulated across scopes.
	const { loadHooks } = await import("./claude/hooks.ts");
	const { runHooks, guardToolCall } = await import("./claude/hook-runner.ts");
	const hookConfig = await loadHooks(cwd);
	const sessionId = session.sessionId;

	// Bound to the TUI below. Hook activity is reported to the USER only: the
	// model must not learn that its command was rewritten, or it starts
	// second-guessing the tool it just called.
	let onHookNotice: ((message: string) => void) | undefined;

	const commands = new CommandRegistry();
	let exiting = false;
	const source = builtinCommands({
		session,
		registry: reg,
		agents,
		onClear: () => {},
		onExit: () => {
			exiting = true;
			process.kill(process.pid, "SIGINT");
		},
	});

	// Registration order IS precedence, least-specific first: a project command
	// shadows a personal one, which shadows a built-in.
	commands.addSource(bindHelp(source, () => commands.list()));

	const { staticSource } = await import("./commands/registry.ts");
	commands.addSource(
		staticSource("builtin", [
			{
				name: "posture",
				description: "Show or change which MCP servers are searchable",
				argumentHint: "[coding|ops|all]",
				run: async ({ args }) => {
					if (!args) {
						const lines = POSTURES.map((p) => {
							const count = mcpTools.filter((t) => inPosture(t, p)).length;
							const mark = p.name === activePosture.name ? "*" : " ";
							return `${mark} ${p.name.padEnd(8)} ${String(count).padStart(3)} tools  ${p.description}`;
						});
						return { output: [`active: ${activePosture.name}`, "", ...lines].join("\n") };
					}
					const next = posture(args.trim());
					if (!next) return { output: `Unknown posture "${args}". Options: ${POSTURES.map((p) => p.name).join(", ")}` };

					activePosture = next;
					// Admitted tools are cleared on switch: they were chosen under the
					// old posture's assumptions, and silently carrying them over would
					// make "which tools are loaded" unanswerable.
					gate.admitted.clear();
					await session.lane.setActiveTools(
						activeToolNames({
							tools: mcpTools,
							posture: activePosture,
							strategy: resolved.tier.toolStrategy,
							state: gate,
							residentTools: RESIDENT,
						}),
						BACKGROUND_CONTEXT,
					);
					const count = mcpTools.filter((t) => inPosture(t, next)).length;
					return { output: `Posture: ${next.name} (${count} tools searchable). Admitted tools cleared.` };
				},
			},
		]),
	);
	commands.addSource(
		skillCommandSource(skills, async (name, args) => {
			const skill = skills.find((s) => s.name === name);
			return args ? `${skill?.content ?? ""}\n\n${args}` : (skill?.content ?? "");
		}),
	);
	const { sessionCommands } = await import("./commands/session-commands.ts");
	commands.addSource(sessionCommands({ session, cwd, sessionsDir: session.sessionsDir, gate: permissionGate }));

	const { renderShellList } = await import("./agent/background-shell.ts");
	commands.addSource({
		origin: "builtin",
		load: async () => [
			{
				origin: "builtin" as const,
				name: "bashes",
				description: "Show background shells and their status",
				run: async () => ({ output: renderShellList(shells.list()) }),
			},
		],
	});

	const { accountCommands } = await import("./commands/account-commands.ts");
	commands.addSource(
		accountCommands({
			registry: reg,
			todos,
			tier: resolved.tier,
			modelLabel: `${provider}/${modelId}`,
		}),
	);

	const { inspectCommands } = await import("./commands/inspect-commands.ts");
	commands.addSource(
		inspectCommands({
			mcpStatuses: () => hub.getStatuses(),
			gate: permissionGate,
			hooks: hookConfig,
			agents,
			tier: resolved.tier,
			cwd,
			modelLabel: `${provider}/${modelId}`,
			activeTools: () => session.lane.getActiveTools(BACKGROUND_CONTEXT),
			settingsLoadedFrom: settings.loadedFrom,
		}),
	);

	commands.addSource(
		staticSource("builtin", [
			{
				name: "todos",
				description: "Show the current todo list",
				run: () => {
					const items = todos.get();
					if (items.length === 0) return { output: "No todos." };
					const mark = { completed: "x", in_progress: "~", pending: " " } as const;
					return { output: items.map((t) => `  [${mark[t.status]}] ${t.content}`).join("\n") };
				},
			},
		]),
	);

	for (const claudeSource of claudeCommandSources(cwd)) commands.addSource(claudeSource);

	// Report what was dropped rather than silently omitting it: instructions the
	// user believes are active but are not is the worst outcome here.
	for (const path of memory.dropped) {
		console.warn(`memory over budget, not loaded: ${path}`);
	}
	if (settings.permissions.defaultMode) {
		process.env.HARNESS_PERMISSION_MODE ??= settings.permissions.defaultMode;
	}

	// SessionStart fires before the first turn. Its stdout becomes context -
	// the relay inbox hook on this machine delivers unread messages that way.
	const sessionStart = await runHooks({
		config: hookConfig,
		event: "SessionStart",
		payload: { session_id: sessionId, transcript_path: session.transcriptPath, cwd },
		onNotice: (n) => onHookNotice?.(n),
	});

	// before_tool can block with a reason. A denial is not an error: the reason
	// goes back to the model and the turn continues, so the user can redirect.
	//
	// Hooks run FIRST, then the gate. The order is a safety property, not a
	// preference: a PreToolUse hook may rewrite the command (the rtk hook turns
	// `git status` into `rtk git status` on every call), and the gate has to
	// judge what will actually execute rather than what the model proposed.
	session.harness.hooks.on("before_tool", async (event) => {
		const result = await guardToolCall({
			config: hookConfig,
			toolName: event.toolName,
			args: (event.args ?? {}) as Record<string, unknown>,
			sessionId,
			transcriptPath: session.transcriptPath,
			cwd,
			check: (req) => permissionGate.check(req),
			primaryArgOf,
			onNotice: (n) => onHookNotice?.(n),
		});
		if (result.blocked) return { block: { reason: result.blocked.reason } };
		// Returning args is what makes a rewrite take effect; without it the
		// hook's decision is observed and then discarded.
		return result.args ? { args: result.args as never } : undefined;
	});

	session.harness.hooks.on("after_tool", async (event) => {
		await runHooks({
			config: hookConfig,
			event: "PostToolUse",
			toolName: event.toolName,
			payload: {
				session_id: sessionId,
				transcript_path: session.transcriptPath,
				cwd,
				tool_name: event.toolName,
				tool_input: (event.args ?? {}) as Record<string, unknown>,
				tool_response: event.content,
			},
			onNotice: (n) => onHookNotice?.(n),
		});
		return undefined;
	});

	// -p / --print: one prompt, emit, exit. Short-circuits before any TUI is
	// built, so it works with no TTY.
	if (argv.print) {
		const { runPrint, formatPrintResult } = await import("./print.ts");
		const format = argv.outputFormat ?? "text";

		// The prompt is the positional text, or stdin when piped.
		const promptText = argv.printPrompt ?? (process.stdin.isTTY ? "" : await readStdin());
		if (!promptText.trim()) {
			console.error('usage: harness -p "your prompt"   (or pipe text on stdin)');
			process.exitCode = 1;
			return;
		}

		// Slash commands run under `-p` too, as they do in Claude Code.
		// `harness -p "/agents"` should print the roster, not ask the model to
		// describe it from the tool catalog - which it will happily do, and get
		// subtly wrong.
		const handledCommand = await commands.execute(promptText.trim());
		if (handledCommand && !handledCommand.prompt) {
			if (handledCommand.output) console.log(handledCommand.output);
			await hub.close();
			return;
		}

		// `@path` inlines files here too. A prompt that behaves differently
		// under `-p` than it does interactively is a trap, and `-p` is where
		// scripts live, so it is the path least able to recover from surprise.
		const { resolveMentions } = await import("./tui/mentions.ts");
		const resolved = await resolveMentions(handledCommand?.prompt ?? promptText, {
			cwd: process.cwd(),
			tier: session.tier,
			roots: permissionGate?.getRoots(),
		});
		for (const m of resolved.mentions) {
			// stderr, not stdout: this is progress, and stdout is piped.
			if (m.skipped) process.stderr.write(`· @${m.raw} - ${m.skipped}\n`);
		}

		const result = await runPrint({
			session,
			prompt: resolved.prompt,
			images: resolved.images.map((i) => ({ type: "image" as const, data: i.data, mimeType: i.mimeType })),
			gate: permissionGate,
			verbose: argv.verbose === true,
			// One JSON object per line, flushed as each event happens - the point
			// of the format is timing, not encoding.
			stream:
				format === "stream-json"
					? (event) => process.stdout.write(`${JSON.stringify(event)}\n`)
					: undefined,
		});
		const rendered = formatPrintResult(result, format);
		if (rendered) console.log(rendered);
		// Print mode returns before the interactive teardown below, so it has to
		// do its own: a background shell started here would otherwise outlive the
		// process that started it, holding a port nobody has a handle on.
		shells.killAll();
		await runHooks({
			config: hookConfig,
			event: "SessionEnd",
			payload: { session_id: sessionId, transcript_path: session.transcriptPath, cwd, reason: "print" },
		});
		await hub.close();
		process.exitCode = result.ok ? 0 : 1;
		return;
	}

	// User keybindings, as Claude Code reads them. Applied before the TUI is
	// built so the editor picks them up.
	const { loadKeybindings } = await import("./claude/keybindings.ts");
	const keys = await loadKeybindings();
	if (keys.error) console.error(`keybindings: ${keys.error}`);
	for (const conflict of keys.conflicts) console.error(`keybindings: ${conflict}`);

	await runApp({
		session,
		commands,
		env,
		gate: permissionGate,
		todos,
		onPlanApprover: (fn) => {
			approvePlan = fn;
		},
		onSubagentEvents: (fn) => {
			onSubagentEvent = fn;
		},
		onHookNotices: (fn) => {
			onHookNotice = fn;
		},
		startupContext: sessionStart.context,
		runPromptHooks: async (prompt) => {
			const out = await runHooks({
				config: hookConfig,
				event: "UserPromptSubmit",
				payload: { session_id: sessionId, transcript_path: session.transcriptPath, cwd, prompt },
				onNotice: (n) => onHookNotice?.(n),
			});
			return { context: out.context, blocked: out.blocked };
		},
		modelLabel: `${provider}/${modelId}`,
		cwd,
		// Mirrors Claude Code's --ax-screen-reader; also the right mode for a
		// dumb terminal or a bad SSH link.
		plain: argv.screenReader === true,
	});
	// Nothing outlives the session: a dev server left running after exit is a
	// port held by a process the user has no handle on.
	shells.killAll();

	// SessionEnd fires on the way out. Awaited despite the teardown: the capture
	// hook on this machine detaches its own worker and returns immediately, and
	// a hook that needs longer has its own timeout.
	await runHooks({
		config: hookConfig,
		event: "SessionEnd",
		payload: { session_id: sessionId, transcript_path: session.transcriptPath, cwd, reason: "exit" },
	});

	// MCP servers are child processes holding open pipes. Only the print path
	// closed them, so the interactive session hung on exit forever — the work
	// was finished and the process simply would not leave.
	await hub.close();

	if (exiting) process.exitCode = 0;

	// Last resort. Everything above is a clean shutdown, but a stdio MCP server
	// that ignores being closed, or any handle a dependency forgot to unref,
	// would stand between the user and their shell with nothing left to do.
	// Two seconds is long enough for a real flush and short enough not to feel
	// like the hang this replaces.
	const forceExit = setTimeout(() => process.exit(process.exitCode ?? 0), 2_000);
	forceExit.unref();
}

// --help and --version answer before anything else is constructed: they must
// work when the config is broken, which is often exactly when they are reached
// for.
if (argv.help) {
	console.log(HELP);
	process.exit(0);
}
if (argv.version) {
	const { readFile } = await import("node:fs/promises");
	const { fileURLToPath } = await import("node:url");
	const pkgPath = fileURLToPath(new URL("../package.json", import.meta.url));
	const pkg = JSON.parse(await readFile(pkgPath, "utf8")) as { version?: string };
	console.log(pkg.version ?? "0.0.0");
	process.exit(0);
}
if (argv.unknown.length > 0) {
	// Reported, not ignored. A script that passes a mistyped flag should learn
	// about it here rather than by not getting the behavior it asked for.
	console.error(`unknown flag(s): ${argv.unknown.join(", ")}`);
	console.error("run `harness --help` for the list");
	// Exit rather than continue: carrying on would run the session under
	// settings the caller did not ask for, which is the failure the message is
	// meant to prevent.
	process.exit(1);
}

switch (command) {
	case undefined:
		await chat();
		break;
	case "providers":
		await listProviders();
		break;
	case "models":
		await listModels();
		break;
	case "login":
		if (!arg) {
			console.error("usage: harness login <provider>");
			process.exitCode = 1;
			break;
		}
		await login(arg);
		break;
	case "logout":
		if (!arg) {
			console.error("usage: harness logout <provider>");
			process.exitCode = 1;
			break;
		}
		await reg.models.logout(arg);
		console.log(`Logged out of ${arg}.`);
		break;
	default:
		console.log(HELP);
		process.exitCode = 1;
}
