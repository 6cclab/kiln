import { readFile } from "node:fs/promises";
import { settingsFiles, type Scope } from "./paths.ts";

/**
 * `.claude/settings.json`.
 *
 * Three sources, merged least- to most-specific: `user`, `project`, `local`
 * (the names `claude --help` uses for `--setting-sources`). Scalars are
 * overridden by later scopes; permission *lists* are concatenated, because a
 * project grant should add to personal grants rather than silently replace
 * them — replacing would quietly revoke permissions the user believes they set.
 */

export type PermissionMode = "manual" | "acceptEdits" | "auto" | "dontAsk" | "bypassPermissions" | "plan";

export interface Permissions {
	allow: string[];
	deny: string[];
	ask: string[];
	defaultMode?: PermissionMode;
}

export interface Settings {
	permissions: Permissions;
	model?: string;
	effortLevel?: string;
	env?: Record<string, string>;
	/** Scopes that actually contributed, for diagnostics. */
	loadedFrom: Scope[];
}

interface RawSettings {
	permissions?: { allow?: string[]; deny?: string[]; ask?: string[]; defaultMode?: PermissionMode };
	model?: string;
	effortLevel?: string;
	env?: Record<string, string>;
}

export interface LoadSettingsOptions {
	/**
	 * Which scopes to read, from `--setting-sources`.
	 *
	 * Omitted means all three. Narrowing is the point of the flag: a CI run that
	 * must not pick up a developer's personal `~/.claude/settings.json` passes
	 * `project` and gets a reproducible configuration.
	 */
	sources?: readonly Scope[];
	/** An extra file read last, from `--settings`. Highest precedence. */
	extra?: string;
}

export async function loadSettings(cwd: string, opts: LoadSettingsOptions = {}): Promise<Settings> {
	const merged: Settings = {
		permissions: { allow: [], deny: [], ask: [] },
		loadedFrom: [],
	};

	const wanted = opts.sources;
	const files = settingsFiles(cwd).filter((f) => !wanted || wanted.includes(f.scope));
	// The `--settings` file is applied last so it overrides the hierarchy, which
	// is what "extra settings file" has to mean to be useful for a one-off run.
	if (opts.extra) files.push({ scope: "local", path: opts.extra });

	for (const { scope, path } of files) {
		let raw: RawSettings;
		try {
			raw = JSON.parse(await readFile(path, "utf8")) as RawSettings;
		} catch (err) {
			// Absent is normal. Malformed is worth knowing about but must not stop
			// startup: losing the whole session to one stray comma is a poor trade.
			if ((err as NodeJS.ErrnoException).code !== "ENOENT") {
				process.emitWarning(`Ignoring unreadable settings at ${path}: ${(err as Error).message}`);
			}
			continue;
		}

		merged.loadedFrom.push(scope);
		merged.permissions.allow.push(...(raw.permissions?.allow ?? []));
		merged.permissions.deny.push(...(raw.permissions?.deny ?? []));
		merged.permissions.ask.push(...(raw.permissions?.ask ?? []));
		if (raw.permissions?.defaultMode) merged.permissions.defaultMode = raw.permissions.defaultMode;
		if (raw.model) merged.model = raw.model;
		if (raw.effortLevel) merged.effortLevel = raw.effortLevel;
		if (raw.env) merged.env = { ...merged.env, ...raw.env };
	}

	return merged;
}

/**
 * Match a tool invocation against a permission rule.
 *
 * Shapes verified against a real `~/.claude/settings.json`:
 *
 *   `Read`             whole tool, by name
 *   `mcp__homelab`     PREFIX - every tool from that MCP server
 *   `Bash(find:*)`     colon form: commands beginning with `find`
 *   `Bash(git *)`      glob form, as documented by `claude --help`
 *
 * Two traps this handles, both found by running it against real data rather
 * than by reading the format:
 *
 *   - **Case.** Claude writes `Read`/`Bash`/`Edit`; pi's tools are `read`,
 *     `bash`, `edit`. Comparing case-sensitively made every rule fail silently,
 *     which fails *open* into prompting for everything.
 *   - **`:*` vs ` *`.** Real files overwhelmingly use the colon form. Treating
 *     it literally meant `Bash(cd:*)` matched nothing.
 *
 * Only `*` is a wildcard; every other regex metacharacter is escaped, so
 * `Bash(npm run build)` cannot match through a `.` the author meant literally.
 */
export function matchesRule(rule: string, toolName: string, primaryArg?: string): boolean {
	const paren = rule.match(/^([^(]+)\(([\s\S]*)\)$/);
	const tool = toolName.toLowerCase();

	if (!paren) {
		const bare = rule.toLowerCase();
		// An `mcp__server` rule covers every tool that server exposes.
		if (bare.startsWith("mcp__")) return tool.startsWith(bare);
		return bare === tool;
	}
	if (paren[1].toLowerCase() !== tool) return false;

	// `find:*` means "the find command, any arguments". Normalize the colon
	// form to the glob form before compiling.
	const glob = paren[2].replace(/:\*$/, " *").trim();

	const pattern = glob
		.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
		.replace(/\\\*/g, ".*")
		// A trailing " .*" should also match the bare command with no arguments,
		// so `Bash(ls:*)` permits plain `ls`.
		.replace(/ \.\*$/, "(\\s.*)?");

	return new RegExp(`^${pattern}$`).test((primaryArg ?? "").trim());
}

/**
 * Tools that cannot change anything.
 *
 * `bash` is deliberately absent: `bash(ls)` is read-only and `bash(rm -rf)` is
 * not, and the difference is in an argument this cannot inspect safely. Erring
 * toward asking is the only correct default there.
 */
const READ_ONLY = new Set(["read", "glob", "grep", "session_search", "tool_search", "bash_output"]);

export type Decision = "allow" | "deny" | "ask";

/**
 * Decide whether a call may proceed.
 *
 * `deny` is checked first and is absolute: an explicit denial must not be
 * overridable by a broader allow rule elsewhere in the hierarchy, or a project
 * file could re-grant something the user deliberately forbade.
 */
export function decide(
	permissions: Permissions,
	toolName: string,
	primaryArg: string | undefined,
	mode: PermissionMode,
): Decision {
	const hits = (rules: string[]) => rules.some((r) => matchesRule(r, toolName, primaryArg));

	if (hits(permissions.deny)) return "deny";
	if (mode === "bypassPermissions") return "allow";
	if (hits(permissions.allow)) return "allow";
	if (hits(permissions.ask)) return "ask";

	switch (mode) {
		case "plan":
			// Read-only: anything that could mutate is refused outright rather
			// than prompted, which is what makes plan mode trustworthy.
			return READ_ONLY.has(toolName) ? "allow" : "deny";
		case "acceptEdits":
			return toolName === "edit" || toolName === "write" ? "allow" : "ask";
		case "dontAsk":
			return "allow";
		case "auto":
			// Claude Code describes `auto` as "a classifier decides what needs
			// asking". There is no classifier here, and inventing one would make
			// the decision unpredictable. What `auto` does instead is the part of
			// that judgement which is not a judgement call: a tool that cannot
			// change anything does not need confirming.
			//
			// Previously this fell through to `manual` and did nothing at all,
			// so cycling into it with Shift+Tab appeared to change the mode while
			// behaving identically.
			return READ_ONLY.has(toolName) ? "allow" : "ask";
		case "manual":
			return "ask";
	}
}
