import type { SelectItem } from "@earendil-works/pi-tui";
import type { CommandSource } from "./registry.ts";
import type { ModalSpec } from "../tui/modal.ts";
import type { PermissionGate } from "../claude/permission.ts";
import type { PermissionMode } from "../claude/settings.ts";
import type { HookConfig, HookEvent } from "../claude/hooks.ts";
import type { AgentDefinition } from "../claude/agents.ts";
import type { McpHub, ServerStatus } from "../mcp/client.ts";
import type { Settings } from "../claude/settings.ts";

/**
 * The commands that manage rather than report: `/permissions`, `/mcp`,
 * `/agents`, `/config`.
 *
 * Each previously printed a listing and stopped, which answered "what is
 * configured" and left the other half — changing it — to a text editor and a
 * restart. These open a panel you navigate and act in.
 *
 * Each returns BOTH a modal and the text summary. Print mode has no TUI, and a
 * command that simply fails there would break `harness -p "/mcp"`, which is a
 * reasonable thing to put in a script.
 */

export interface ManageDeps {
	gate: PermissionGate;
	hub: McpHub;
	hooks: HookConfig;
	agents: readonly AgentDefinition[];
	settings: Settings;
	cwd: string;
	modelLabel: string;
	/** Persist a rule to `.claude/settings.local.json`. */
	saveRule: (list: "allow" | "deny" | "ask", rule: string) => Promise<void>;
	removeRule: (list: "allow" | "deny" | "ask", rule: string) => Promise<void>;
}

const MODES: PermissionMode[] = ["manual", "acceptEdits", "auto", "plan", "dontAsk", "bypassPermissions"];

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

function truncate(text: string, max: number): string {
	return text.length <= max ? text : `${text.slice(0, max - 1)}…`;
}

/**
 * Permission rules, grouped and editable.
 *
 * Deny is listed first because that is the order the decision is made in: an
 * explicit denial beats everything, so reading the panel top to bottom matches
 * how a call is actually judged.
 *
 * Rules added here are written to `.claude/settings.local.json`, never to the
 * shared `settings.json`. A rule granted mid-task to unblock yourself should
 * not quietly become the project's policy for everyone.
 */
function permissionsModal(deps: ManageDeps): ModalSpec {
	const rows = (): SelectItem[] => {
		const permissions = deps.gate.getPermissions();
		const out: SelectItem[] = [];
		for (const list of ["deny", "allow", "ask"] as const) {
			for (const rule of permissions[list]) {
				out.push({ value: `${list}:${rule}`, label: rule, description: list });
			}
		}
		for (const grant of deps.gate.getSessionGrants()) {
			// Short enough to survive the description column at a normal width;
			// "granted this session, not saved" was truncated mid-word.
			out.push({ value: `session:${grant}`, label: grant, description: "session only" });
		}
		return out;
	};

	return {
		title: "Permissions",
		items: rows,
		empty: "No rules. Every tool call is judged by the mode alone.",
		header: () => [
			`mode       ${deps.gate.mode}`,
			`workspace  ${deps.gate.getRoots().join(", ")}`,
			`settings   ${deps.settings.loadedFrom.join(", ") || "none"}`,
		],
		actions: [
			{
				key: "m",
				label: "cycle mode",
				run: () => {
					const next = MODES[(MODES.indexOf(deps.gate.mode) + 1) % MODES.length];
					deps.gate.setMode(next);
					return `mode is now ${next}`;
				},
			},
			{
				key: "d",
				label: "delete rule",
				run: async (selected) => {
					if (!selected) return undefined;
					const [list, ...rest] = selected.value.split(":");
					const rule = rest.join(":");
					if (list === "session") {
						return "session grants clear when the session ends; nothing to delete";
					}
					await deps.removeRule(list as "allow" | "deny" | "ask", rule);
					return `removed ${rule} from ${list}`;
				},
			},
			{
				key: "p",
				label: "promote to deny",
				run: async (selected) => {
					if (!selected) return undefined;
					const rule = selected.value.split(":").slice(1).join(":");
					// The common move after seeing something you did not expect to
					// be allowed, and the one that otherwise means editing a file.
					await deps.saveRule("deny", rule);
					return `${rule} is now denied`;
				},
			},
		],
	};
}

/** MCP servers, with the failures that currently scroll past at startup. */
function mcpModal(deps: ManageDeps): ModalSpec {
	const rows = (): SelectItem[] =>
		[...deps.hub.getStatuses()]
			.sort((a, b) => Number(b.ok) - Number(a.ok) || b.toolCount - a.toolCount)
			.map((s: ServerStatus) => ({
				value: s.name,
				label: s.name,
				description: s.ok ? `${s.toolCount} tools · ${s.ms}ms` : `failed: ${truncate(s.error ?? "unknown", 70)}`,
			}));

	return {
		title: "MCP servers",
		items: rows,
		empty: "No MCP servers configured. They are read from ~/.claude.json",
		header: () => {
			const statuses = deps.hub.getStatuses();
			const ok = statuses.filter((s) => s.ok);
			return [`${ok.length}/${statuses.length} connected · ${ok.reduce((n, s) => n + s.toolCount, 0)} tools`];
		},
		actions: [
			{
				key: "t",
				label: "list tools",
				run: (selected) => {
					if (!selected) return undefined;
					const names = deps.hub
						.getTools()
						.filter((t) => (t as { server?: string }).server === selected.value)
						.map((t) => (t as { name: string }).name);
					return names.length > 0 ? truncate(names.join(", "), 400) : "no tools from this server";
				},
			},
		],
	};
}

/** Subagents, with the prompt each one actually runs on. */
function agentsModal(deps: ManageDeps): ModalSpec {
	return {
		title: "Subagents",
		items: () =>
			deps.agents.map((a) => ({
				value: a.name,
				label: a.name,
				description: `${a.model ?? "inherit"} · ${a.tools ? `${a.tools.length} tools` : "all tools"} · ${truncate(a.description, 60)}`,
			})),
		empty: "No subagents. Define them in .claude/agents/*.md",
		header: () => [`dispatched with the task tool · defined in .claude/agents`],
		actions: [
			{
				key: "s",
				label: "show prompt",
				run: (selected) => {
					const agent = deps.agents.find((a) => a.name === selected?.value);
					// The system prompt is the agent: two definitions with the same
					// description can behave completely differently, and this is the
					// only place that difference is visible.
					return agent ? truncate(agent.prompt.replace(/\s+/g, " "), 400) : undefined;
				},
			},
			{
				key: "w",
				label: "where defined",
				run: (selected) => deps.agents.find((a) => a.name === selected?.value)?.path,
			},
		],
	};
}

/** Everything that was loaded, and from where. */
function configModal(deps: ManageDeps): ModalSpec {
	return {
		title: "Configuration",
		items: () => {
			const hookCount = HOOK_EVENTS.reduce(
				(n, e) => n + (deps.hooks[e] ?? []).reduce((m, g) => m + (g.hooks?.length ?? 0), 0),
				0,
			);
			const permissions = deps.gate.getPermissions();
			return [
				{ value: "model", label: "model", description: deps.modelLabel },
				{ value: "mode", label: "permission mode", description: deps.gate.mode },
				{ value: "workspace", label: "workspace roots", description: deps.gate.getRoots().join(", ") },
				{ value: "settings", label: "settings loaded from", description: deps.settings.loadedFrom.join(", ") || "none" },
				{
					value: "rules",
					label: "permission rules",
					description: `${permissions.allow.length} allow · ${permissions.deny.length} deny · ${permissions.ask.length} ask`,
				},
				{ value: "hooks", label: "hooks", description: `${hookCount} across ${HOOK_EVENTS.filter((e) => deps.hooks[e]?.length).length} events` },
				{ value: "agents", label: "subagents", description: `${deps.agents.length} available` },
				{
					value: "mcp",
					label: "mcp servers",
					description: `${deps.hub.getStatuses().filter((s) => s.ok).length}/${deps.hub.getStatuses().length} connected`,
				},
				{ value: "cwd", label: "working directory", description: deps.cwd },
			];
		},
		header: () => ["read-only; each line says where the value came from"],
	};
}

export function manageCommands(deps: ManageDeps): CommandSource {
	const screens: Array<{ name: string; description: string; spec: () => ModalSpec; text: () => string }> = [
		{
			name: "permissions",
			description: "View and edit permission rules",
			spec: () => permissionsModal(deps),
			text: () => {
				const p = deps.gate.getPermissions();
				return [
					`mode       ${deps.gate.mode}`,
					`workspace  ${deps.gate.getRoots().join(", ")}`,
					"",
					...(["deny", "allow", "ask"] as const).flatMap((list) =>
						p[list].length ? [`${list} (${p[list].length})`, ...p[list].map((r) => `  ${r}`)] : [],
					),
				].join("\n");
			},
		},
		{
			name: "mcp",
			description: "View MCP servers and their tools",
			spec: () => mcpModal(deps),
			text: () => {
				const statuses = deps.hub.getStatuses();
				const ok = statuses.filter((s) => s.ok);
				return [
					`${ok.length}/${statuses.length} connected, ${ok.reduce((n, s) => n + s.toolCount, 0)} tools`,
					"",
					...statuses.map((s) =>
						s.ok
							? `  ${s.name.padEnd(22)} ${String(s.toolCount).padStart(4)} tools  ${s.ms}ms`
							: `  ${s.name.padEnd(22)} failed: ${truncate(s.error ?? "unknown", 90)}`,
					),
				].join("\n");
			},
		},
		{
			name: "agents",
			description: "View the subagents available for dispatch",
			spec: () => agentsModal(deps),
			text: () =>
				deps.agents.length === 0
					? "No subagents. Define them in .claude/agents/*.md"
					: [
							`${deps.agents.length} available:`,
							...deps.agents.map((a) => `  ${a.name}${a.model ? ` (${a.model})` : ""}\n    ${a.description}`),
						].join("\n"),
		},
		{
			name: "config",
			description: "Show settings and where they came from",
			spec: () => configModal(deps),
			text: () =>
				[
					`model      ${deps.modelLabel}`,
					`mode       ${deps.gate.mode}`,
					`workspace  ${deps.gate.getRoots().join(", ")}`,
					`settings   ${deps.settings.loadedFrom.join(", ") || "none"}`,
					`agents     ${deps.agents.length}`,
				].join("\n"),
		},
	];

	return {
		origin: "builtin",
		load: async () =>
			screens.map((s) => ({
				origin: "builtin" as const,
				name: s.name,
				description: s.description,
				run: async () => ({
					// Both: the TUI opens the panel, print mode falls back to the
					// text so `harness -p "/mcp"` stays useful in a script.
					modal: s.spec(),
					output: s.text(),
				}),
			})),
	};
}
