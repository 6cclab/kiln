import { readdir, readFile } from "node:fs/promises";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { homedir } from "node:os";
import { claudeRoots, CLAUDE_MD, type Scope } from "./paths.ts";

/**
 * `CLAUDE.md` memory.
 *
 * Two behaviors that are easy to miss and both present in real files:
 *
 *   - **`@path` imports.** A line like `@RTK.md` pulls in another file. Paths
 *     resolve relative to the importing file, and `~` expands to home.
 *   - **A budget.** Memory competes with tools and conversation for the same
 *     window. On the `small` tier the whole system prompt is capped at ~2k
 *     tokens, and an unbounded CLAUDE.md would silently eat the session.
 *
 * When the budget is exceeded, the *lowest-priority* content is dropped and the
 * user is told. Truncating silently would mean instructions that appear to be
 * in effect but are not — worse than not loading them at all.
 */

const MAX_IMPORT_DEPTH = 5;

/** Markdown files in a `.claude/rules` directory, sorted for stable ordering. */
async function listRules(dir: string): Promise<string[]> {
	try {
		const entries = await readdir(dir, { withFileTypes: true });
		return entries
			.filter((e) => e.isFile() && e.name.endsWith(".md"))
			.map((e) => join(dir, e.name))
			.sort();
	} catch {
		// No rules directory is the normal case.
		return [];
	}
}

export interface MemoryFile {
	path: string;
	scope: Scope;
	content: string;
}

export interface AssembledMemory {
	text: string;
	files: MemoryFile[];
	/** Files dropped because the budget was exhausted. Surface these to the user. */
	dropped: string[];
	estimatedTokens: number;
}

/** Rough token estimate. Only used for budgeting, never reported as exact. */
function estimate(text: string): number {
	return Math.ceil(text.length / 4);
}

function expandHome(path: string): string {
	return path.startsWith("~/") ? join(homedir(), path.slice(2)) : path;
}

/**
 * Resolve `@path` imports recursively.
 *
 * `seen` guards against cycles: two files importing each other would otherwise
 * recurse until the stack blows, and a self-import is an easy typo.
 */
async function resolveImports(content: string, fromFile: string, seen: Set<string>, depth = 0): Promise<string> {
	if (depth >= MAX_IMPORT_DEPTH) return content;

	const lines = content.split("\n");
	const out: string[] = [];

	for (const line of lines) {
		// Only a line that is *just* an import counts. An inline "@foo" in prose
		// (an email address, a handle) must not trigger a file read.
		const match = line.match(/^@([^\s]+)\s*$/);
		if (!match) {
			out.push(line);
			continue;
		}

		const target = expandHome(match[1]);
		const path = isAbsolute(target) ? target : resolve(dirname(fromFile), target);
		if (seen.has(path)) {
			out.push(`<!-- skipped circular import: ${match[1]} -->`);
			continue;
		}
		seen.add(path);

		try {
			const imported = await readFile(path, "utf8");
			out.push(await resolveImports(imported, path, seen, depth + 1));
		} catch {
			// A broken import is worth surfacing in-band: the instructions the
			// author expected are absent, and silence would hide that.
			out.push(`<!-- missing import: ${match[1]} -->`);
		}
	}
	return out.join("\n");
}

/**
 * Load and assemble memory within a token budget.
 *
 * Order is least- to most-specific (user, then project), and the budget is
 * spent in reverse: project memory is the most situational and survives, while
 * broad personal preferences are dropped first if something must go.
 */
export async function loadMemory(cwd: string, budgetTokens: number): Promise<AssembledMemory> {
	const found: MemoryFile[] = [];

	for (const root of claudeRoots(cwd)) {
		// `~/.claude/CLAUDE.md` for user scope; `<project>/CLAUDE.md` at the repo
		// root for project scope - note the project file sits beside `.claude`,
		// not inside it.
		const path = root.scope === "user" ? join(root.dir, CLAUDE_MD) : join(cwd, CLAUDE_MD);
		try {
			const raw = await readFile(path, "utf8");
			found.push({ path, scope: root.scope, content: await resolveImports(raw, path, new Set([path])) });
		} catch {
			// No memory file at this scope is the normal case.
		}

		// `.claude/rules/*.md` are loaded as memory too, without needing an
		// explicit import. Confirmed by observing a live Claude Code session's
		// context, which carried `rules/evidence.md` and
		// `rules/surface-failures.md` alongside CLAUDE.md.
		for (const rule of await listRules(join(root.dir, "rules"))) {
			try {
				const raw = await readFile(rule, "utf8");
				found.push({ path: rule, scope: root.scope, content: await resolveImports(raw, rule, new Set([rule])) });
			} catch {
				// Unreadable rule: skip rather than fail the whole assembly.
			}
		}
	}

	const kept: MemoryFile[] = [];
	const dropped: string[] = [];
	let used = 0;

	// Reverse so the most specific scope claims budget first.
	for (const file of [...found].reverse()) {
		const cost = estimate(file.content);
		if (used + cost > budgetTokens) {
			dropped.push(file.path);
			continue;
		}
		kept.unshift(file);
		used += cost;
	}

	const text = kept
		.map((f) => `<memory path="${f.path}" scope="${f.scope}">\n${f.content.trim()}\n</memory>`)
		.join("\n\n");

	return { text, files: kept, dropped, estimatedTokens: used };
}
