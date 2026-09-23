import { readdir, readFile } from "node:fs/promises";
import { join, relative, sep } from "node:path";
import { parse as parseYaml } from "yaml";
import type { Command, CommandOrigin, CommandSource } from "../commands/registry.ts";
import { claudeRoots } from "./paths.ts";

/**
 * `.claude/commands/*.md` as slash commands.
 *
 * Verified against a real command file (`~/.claude/commands/track-work.md`):
 * frontmatter carries `description` but NOT `name` — the command name comes
 * from the filename. A nested path becomes a namespace, so
 * `commands/frontend/component.md` is `/frontend:component`.
 *
 * The body is a prompt template, not output: running the command sends the body
 * to the model. That is why `CommandResult` distinguishes `output` (shown) from
 * `prompt` (sent) — these commands only ever produce the latter.
 */

interface CommandFrontmatter {
	description?: string;
	"argument-hint"?: string;
	/** Reserved: honored once Phase 5 gating can scope tools per command. */
	"allowed-tools"?: string | string[];
	model?: string;
}

/** Split `---\n...\n---\n<body>`. Returns the whole input as body when absent. */
export function splitFrontmatter(source: string): { data: CommandFrontmatter; body: string } {
	const match = source.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n?([\s\S]*)$/);
	if (!match) return { data: {}, body: source };
	try {
		return { data: (parseYaml(match[1]) ?? {}) as CommandFrontmatter, body: match[2] };
	} catch {
		// Malformed YAML: keep the body usable rather than dropping the command.
		// A command that runs without its description beats one that vanishes.
		return { data: {}, body: match[2] };
	}
}

/**
 * Substitute arguments into a command body.
 *
 * `$ARGUMENTS` takes everything; `$1`, `$2`, ... take positional words. Both
 * forms exist in Claude Code's command files, and a template using neither is
 * left alone — the arguments are then appended, so a bare prompt template still
 * receives what the user typed instead of silently discarding it.
 */
export function applyArguments(body: string, args: string): string {
	const positional = args.split(/\s+/).filter(Boolean);
	const usesPlaceholders = /\$ARGUMENTS|\$\d/.test(body);

	let out = body.replace(/\$ARGUMENTS/g, args);
	out = out.replace(/\$(\d+)/g, (_, n: string) => positional[Number(n) - 1] ?? "");

	if (!usesPlaceholders && args) out = `${out}\n\n${args}`;
	return out;
}

async function walkMarkdown(dir: string, base = dir): Promise<string[]> {
	let entries;
	try {
		entries = await readdir(dir, { withFileTypes: true });
	} catch {
		// A missing commands directory is the normal case, not an error.
		return [];
	}
	const found: string[] = [];
	for (const entry of entries) {
		const full = join(dir, entry.name);
		if (entry.isDirectory()) found.push(...(await walkMarkdown(full, base)));
		else if (entry.name.endsWith(".md")) found.push(full);
	}
	return found;
}

async function loadFrom(dir: string, origin: CommandOrigin): Promise<Command[]> {
	const files = await walkMarkdown(dir);

	return Promise.all(
		files.map(async (file) => {
			const source = await readFile(file, "utf8");
			const { data, body } = splitFrontmatter(source);

			// `commands/frontend/component.md` -> namespace "frontend", name "component".
			const rel = relative(dir, file).replace(/\.md$/, "");
			const segments = rel.split(sep);
			const name = segments.pop() as string;
			const namespace = segments.length ? segments.join(":") : undefined;

			return {
				name,
				namespace,
				origin,
				description: data.description,
				argumentHint: data["argument-hint"],
				// The body becomes the prompt; nothing is printed to the transcript.
				run: ({ args }) => ({ prompt: applyArguments(body.trim(), args) }),
			} satisfies Command;
		}),
	);
}

/**
 * One source per scope, so precedence is expressed by registration order rather
 * than by sorting inside a single source.
 */
export function claudeCommandSources(cwd: string): CommandSource[] {
	return claudeRoots(cwd).map((root) => ({
		origin: root.scope === "user" ? ("personal" as const) : ("project" as const),
		load: () => loadFrom(join(root.dir, "commands"), root.scope === "user" ? "personal" : "project"),
	}));
}
