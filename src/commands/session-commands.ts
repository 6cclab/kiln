import { spawn } from "node:child_process";
import { writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import { BACKGROUND_CONTEXT, JsonlSessionRepo } from "@earendil-works/pi-agent-core";
import { NodeExecutionEnv } from "@earendil-works/pi-agent-core/node";
import type { StartedSession } from "../agent/session.ts";
import { settingsFiles } from "../claude/paths.ts";
import { staticSource, type Command, type CommandSource } from "./registry.ts";

/**
 * Session-lifecycle commands: `/resume`, `/rewind`, `/export`, `/memory`.
 *
 * `/resume` deliberately lists rather than swaps. Resuming means replacing the
 * session, tools, model and gate — effectively rebuilding everything the
 * process holds. Doing that in-place would leave half-torn-down state behind on
 * any failure, so the session id is printed and the restart flag named instead.
 * `harness --resume <id>` is the supported path.
 */

export interface SessionCommandDeps {
	session: StartedSession;
	/** Permission gate, for /add-dir. */
	gate?: { addRoot(dir: string): string; getRoots(): readonly string[] };
	cwd: string;
	sessionsDir: string;
}

function ago(ms: number): string {
	const seconds = Math.floor((Date.now() - ms) / 1000);
	if (seconds < 60) return `${seconds}s ago`;
	if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
	if (seconds < 86_400) return `${Math.floor(seconds / 3600)}h ago`;
	return `${Math.floor(seconds / 86_400)}d ago`;
}

/** Readable text from an entry, ignoring blocks that have no prose. */
function entryText(entry: unknown): { role: string; text: string } | undefined {
	const message = (entry as { message?: { role?: string; content?: unknown } }).message;
	if (!message?.role) return undefined;

	if (typeof message.content === "string") return { role: message.role, text: message.content };
	if (!Array.isArray(message.content)) return undefined;

	const text = message.content
		.filter((b): b is { type: "text"; text: string } => (b as { type?: string })?.type === "text")
		.map((b) => b.text)
		.join("\n")
		.trim();
	return text ? { role: message.role, text } : undefined;
}

export function sessionCommands(deps: SessionCommandDeps): CommandSource {
	const { session, cwd, sessionsDir } = deps;

	const commands: Omit<Command, "origin">[] = [
		{
			name: "resume",
			description: "List past sessions in this directory",
			argumentHint: "[session-id]",
			run: async ({ args }) => {
				if (args) {
					return {
						output:
							`Resuming replaces the session, tools and model, so it happens at startup:\n\n` +
							`  harness --resume ${args}\n`,
					};
				}

				const repo = new JsonlSessionRepo({
					fileSystem: new NodeExecutionEnv({ cwd }),
					sessionsRoot: sessionsDir,
				});
				const found = await repo.list({ cwd }, BACKGROUND_CONTEXT);
				if (found.length === 0) return { output: "No past sessions in this directory." };

				const rows = found
					.sort((a, b) => b.modifiedAt - a.modifiedAt)
					.slice(0, 15)
					.map((m) => `  ${m.id}  ${ago(m.modifiedAt).padStart(8)}`);

				return { output: [`${found.length} session(s):`, "", ...rows, "", "Resume: harness --resume <id>"].join("\n") };
			},
		},
		{
			name: "rewind",
			description: "Go back to an earlier point in this conversation",
			argumentHint: "[entry-id]",
			run: async ({ args }) => {
				const entries = await session.lane.findEntries(undefined, BACKGROUND_CONTEXT);

				if (!args) {
					// Only user turns are useful rewind targets: they are the points a
					// person actually recognizes, unlike intermediate tool results.
					const turns = entries
						.map((entry) => ({ entry, parsed: entryText(entry) }))
						.filter((e) => e.parsed?.role === "user")
						.slice(-10);

					if (turns.length === 0) return { output: "Nothing to rewind to yet." };

					const rows = turns.map(({ entry, parsed }) => {
						const id = (entry as { id?: string }).id ?? "?";
						return `  ${id}  ${(parsed?.text ?? "").replace(/\s+/g, " ").slice(0, 60)}`;
					});
					return { output: ["Rewind to which turn?", "", ...rows, "", "Use: /rewind <entry-id>"].join("\n") };
				}

				const result = await session.lane.navigateTree(args.trim(), undefined, BACKGROUND_CONTEXT);
				return { output: `Rewound to ${args.trim()}. ${JSON.stringify(result).slice(0, 120)}` };
			},
		},
		{
			name: "export",
			description: "Write this conversation to a markdown file",
			argumentHint: "[path]",
			run: async ({ args }) => {
				const entries = await session.lane.findEntries(undefined, BACKGROUND_CONTEXT);
				const parts: string[] = [`# Session transcript`, "", `- directory: \`${cwd}\``, ""];

				for (const entry of entries) {
					const parsed = entryText(entry);
					if (!parsed) continue;
					parts.push(`## ${parsed.role}`, "", parsed.text, "");
				}

				// Default beside the project rather than in a temp dir: an export the
				// user cannot find later is not an export.
				const target = resolve(cwd, args.trim() || `transcript-${Date.now()}.md`);
				await writeFile(target, parts.join("\n"));
				return { output: `Wrote ${entries.length} entries to ${target}` };
			},
		},
		{
			name: "memory",
			description: "Open CLAUDE.md in your editor",
			argumentHint: "[user|project]",
			run: ({ args }) => {
				const scope = args.trim() || "project";
				const path =
					scope === "user" ? join(homedir(), ".claude", "CLAUDE.md") : join(cwd, "CLAUDE.md");

				const editor = process.env.VISUAL ?? process.env.EDITOR;
				if (!editor) {
					// Naming the file beats failing: the user can open it themselves.
					return { output: `No $EDITOR set. Edit manually:\n  ${path}` };
				}

				// detached + unref so a long-lived editor does not hold the harness
				// hostage, and stdio inherited so a terminal editor actually works.
				spawn(editor, [path], { stdio: "inherit", detached: true }).unref();
				return { output: `Opened ${path} in ${editor}.` };
			},
		},
		{
			name: "add-dir",
			description: "Allow tools to work in another directory",
			argumentHint: "<path>",
			run: ({ args }) => {
				if (!deps.gate) return { output: "Workspace roots are not enforced in this session." };
				if (!args) {
					return { output: ["Workspace roots:", ...deps.gate.getRoots().map((r) => `  ${r}`)].join("\n") };
				}
				const added = deps.gate.addRoot(resolve(cwd, args.trim()));
				return { output: `Added ${added}. Tools may now work there without prompting.` };
			},
		},
		{
			name: "init",
			description: "Generate a CLAUDE.md describing this codebase",
			run: () => ({
				// A prompt, not output: the model has the tools to inspect the repo,
				// and hardcoding an analysis here would just be a worse version of
				// what it can work out by looking.
				prompt: [
					"Analyze this codebase and write a CLAUDE.md at the project root.",
					"",
					"Cover: what the project does, how to build/test/run it, the layout of",
					"the main directories, and any conventions a newcomer would otherwise",
					"get wrong. Read the actual files - package manifests, configs, a few",
					"representative sources - rather than guessing from names.",
					"",
					"Write instructions, not history: no changelog, no dated notes.",
					"If a CLAUDE.md already exists, improve it rather than replacing it.",
				].join("\n"),
			}),
		},
		{
			name: "config",
			description: "Show settings and where they came from",
			run: () => {
				const files = settingsFiles(cwd).map(({ scope, path }) => `  ${scope.padEnd(8)} ${path}`);
				return {
					output: [
						"Settings are read in this order, later overriding earlier:",
						"",
						...files,
						"",
						"Edit a file directly, then restart. Permission lists concatenate",
						"across scopes rather than replacing.",
					].join("\n"),
				};
			},
		},
	];

	return staticSource("builtin", commands);
}
