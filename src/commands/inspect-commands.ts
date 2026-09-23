import type { CommandSource } from "./registry.ts";
import type { ServerStatus } from "../mcp/client.ts";
import type { PermissionGate } from "../claude/permission.ts";
import type { HookConfig, HookEvent } from "../claude/hooks.ts";
import type { AgentDefinition } from "../claude/agents.ts";
import type { Tier } from "../budget/tier.ts";

/**
 * Inspection commands: `/mcp`, `/permissions`, `/hooks`, `/doctor`.
 *
 * All four answer the same class of question — "what is actually configured
 * right now?" — and all four exist because the answer is otherwise spread
 * across four files in three directories, two of which the harness merges
 * before using.
 *
 * `/doctor` is the one that earns its place here rather than in a script. Two
 * MCP servers on this machine fail on every startup (`proxmox` is missing a
 * `tsx` binary, `sentry` needs OAuth), and that currently surfaces as two lines
 * of stderr that scroll away before the first prompt.
 */

export interface InspectDeps {
	mcpStatuses: () => readonly ServerStatus[];
	gate: PermissionGate;
	hooks: HookConfig;
	agents: readonly AgentDefinition[];
	tier: Tier;
	cwd: string;
	modelLabel: string;
	/** Resident tool names, for the doctor's summary. */
	activeTools: () => Promise<string[]>;
	settingsLoadedFrom: string[];
}

const HOOK_EVENTS: HookEvent[] = [
	"PreToolUse",
	"PostToolUse",
	"UserPromptSubmit",
	"SessionStart",
	"SessionEnd",
	"Stop",
	"SubagentStop",
	"Notification",
	"PreCompact",
];

/** Events the harness parses but does not yet fire. Saying so beats implying otherwise. */
const UNFIRED = new Set<HookEvent>(["Stop", "SubagentStop", "Notification", "PreCompact"]);

function truncate(text: string, max: number): string {
	return text.length <= max ? text : `${text.slice(0, max - 1)}…`;
}

export function inspectCommands(deps: InspectDeps): CommandSource {
	return {
		origin: "builtin",
		load: async () => [
			{
				origin: "builtin" as const,
				name: "mcp",
				description: "Show MCP servers, their status and tool counts",
				run: async () => {
					const statuses = deps.mcpStatuses();
					if (statuses.length === 0) {
						return { output: "No MCP servers configured. They are read from ~/.claude.json" };
					}
					const ok = statuses.filter((s) => s.ok);
					const failed = statuses.filter((s) => !s.ok);
					const tools = ok.reduce((n, s) => n + s.toolCount, 0);

					const lines = [`${ok.length}/${statuses.length} connected, ${tools} tools`, ""];
					for (const s of [...ok].sort((a, b) => b.toolCount - a.toolCount)) {
						lines.push(`  ${s.name.padEnd(22)} ${String(s.toolCount).padStart(4)} tools  ${s.ms}ms`);
					}
					for (const s of failed) {
						// The error is the whole point of listing a failed server.
						lines.push(`  ${s.name.padEnd(22)} failed: ${truncate(s.error ?? "unknown", 90)}`);
					}
					return { output: lines.join("\n") };
				},
			},
			{
				origin: "builtin" as const,
				name: "permissions",
				description: "Show permission mode, rules and this session's grants",
				run: async () => {
					const p = deps.gate.getPermissions();
					const grants = deps.gate.getSessionGrants();
					const lines = [
						`mode       ${deps.gate.mode}`,
						`settings   ${deps.settingsLoadedFrom.join(", ") || "none"}`,
						"",
						`workspace  ${deps.gate.getRoots().join("\n           ")}`,
					];

					const rules = (label: string, list: string[]) => {
						if (list.length === 0) return;
						lines.push("", `${label} (${list.length})`, ...list.map((r) => `  ${r}`));
					};
					// Deny first: it wins over everything else, so reading it first
					// matches how the decision is actually made.
					rules("deny", p.deny);
					rules("allow", p.allow);
					rules("ask", p.ask);

					if (grants.length > 0) {
						lines.push(
							"",
							`granted this session (${grants.length}) - not saved to settings`,
							...grants.map((g) => `  ${g}`),
						);
					}
					return { output: lines.join("\n") };
				},
			},
			{
				origin: "builtin" as const,
				name: "hooks",
				description: "Show configured hooks and which events they fire on",
				run: async () => {
					const configured = HOOK_EVENTS.filter((e) => (deps.hooks[e]?.length ?? 0) > 0);
					if (configured.length === 0) {
						return { output: "No hooks configured. They are read from .claude/settings.json" };
					}
					const lines: string[] = [];
					for (const event of configured) {
						const note = UNFIRED.has(event) ? "  (parsed, not yet fired by this harness)" : "";
						lines.push(`${event}${note}`);
						for (const group of deps.hooks[event] ?? []) {
							const matcher = group.matcher ? `[${group.matcher}] ` : "";
							for (const hook of group.hooks ?? []) {
								const timeout = hook.timeout ? ` (${hook.timeout}s)` : "";
								lines.push(`  ${matcher}${truncate(hook.command, 100)}${timeout}`);
							}
						}
						lines.push("");
					}
					return { output: lines.join("\n").trimEnd() };
				},
			},
			{
				origin: "builtin" as const,
				name: "doctor",
				description: "Check the setup and report anything broken",
				run: async () => {
					const lines = [`model      ${deps.modelLabel}`, `tier       ${deps.tier.name} (${deps.tier.contextWindow} tokens)`];

					const active = await deps.activeTools();
					lines.push(`tools      ${active.length} resident, strategy "${deps.tier.toolStrategy}"`);

					const statuses = deps.mcpStatuses();
					const failed = statuses.filter((s) => !s.ok);
					lines.push(`mcp        ${statuses.length - failed.length}/${statuses.length} connected`);

					const hookCount = HOOK_EVENTS.reduce(
						(n, e) => n + (deps.hooks[e] ?? []).reduce((m, g) => m + (g.hooks?.length ?? 0), 0),
						0,
					);
					lines.push(`hooks      ${hookCount} across ${HOOK_EVENTS.filter((e) => deps.hooks[e]?.length).length} events`);
					lines.push(`agents     ${deps.agents.length} available`);
					lines.push(`settings   ${deps.settingsLoadedFrom.join(", ") || "none"}`);

					// Problems last, so they are what is left on screen.
					const problems: string[] = [];
					for (const s of failed) problems.push(`mcp "${s.name}" is down: ${truncate(s.error ?? "unknown", 100)}`);
					if (active.length === 0) problems.push("no tools are resident - the model cannot act");
					if (deps.gate.mode === "bypassPermissions") {
						problems.push("permission mode is bypassPermissions: every tool call runs unchecked");
					}

					lines.push("", problems.length === 0 ? "No problems found." : `${problems.length} problem(s):`);
					for (const p of problems) lines.push(`  - ${p}`);

					return { output: lines.join("\n") };
				},
			},
		],
	};
}
