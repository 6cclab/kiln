import { homedir } from "node:os";
import { join } from "node:path";

/**
 * The `.claude` directory hierarchy.
 *
 * `claude --help` documents three setting sources — `user`, `project`, `local` —
 * and the same three-tier shape governs commands, skills and memory. Ordering is
 * least-specific first throughout, so a later tier shadows an earlier one. The
 * command registry already resolves collisions that way, so a project command
 * overriding a personal one needs no special handling.
 */

export type Scope = "user" | "project" | "local";

export interface ClaudeRoot {
	scope: Scope;
	/** Absolute path to a `.claude` directory. May not exist. */
	dir: string;
}

/**
 * Roots in precedence order, lowest first.
 *
 * `local` is the project's `.claude/settings.local.json` sibling space —
 * gitignored, machine-specific. It shares the project directory rather than
 * having one of its own, so only the settings file differs.
 */
export function claudeRoots(cwd: string): ClaudeRoot[] {
	return [
		{ scope: "user", dir: join(homedir(), ".claude") },
		{ scope: "project", dir: join(cwd, ".claude") },
	];
}

export const CLAUDE_MD = "CLAUDE.md";

export function settingsFiles(cwd: string): Array<{ scope: Scope; path: string }> {
	return [
		{ scope: "user", path: join(homedir(), ".claude", "settings.json") },
		{ scope: "project", path: join(cwd, ".claude", "settings.json") },
		// Machine-local overrides, gitignored. Highest precedence.
		{ scope: "local", path: join(cwd, ".claude", "settings.local.json") },
	];
}
