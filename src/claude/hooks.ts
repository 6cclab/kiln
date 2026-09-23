import { readFile } from "node:fs/promises";
import { settingsFiles } from "./paths.ts";

/**
 * `.claude/settings.json` hooks.
 *
 * A hook is a shell command the harness runs at a defined point, handed a JSON
 * payload on stdin. It can observe, it can inject context, and — for
 * `PreToolUse` — it can rewrite or refuse the tool call.
 *
 * ## The contract, read from real hooks rather than from documentation
 *
 * Taken from this user's own `~/.claude/hooks/`:
 *
 *   - `rtk-rewrite.sh` reads `.tool_input.command`, rewrites it, and returns
 *     `hookSpecificOutput.updatedInput` with `permissionDecision: "allow"`.
 *     **A hook can change what actually runs**, which makes this the one
 *     integration point where getting the shape wrong silently executes the
 *     wrong command.
 *   - `homelab-kb-capture.sh` reads `session_id` and `transcript_path` on
 *     `SessionEnd`, detaches, and exits immediately.
 *   - The relay `UserPromptSubmit` hook `curl`s an inbox summary and writes
 *     **plain text** to stdout — not JSON. That text becomes context for the
 *     turn. So stdout must not be required to parse as JSON.
 *
 * ## Config shape
 *
 * ```json
 * "PreToolUse": [
 *   { "matcher": "Bash",
 *     "hooks": [{ "type": "command", "command": "...", "timeout": 15 }] }
 * ]
 * ```
 *
 * `matcher` is a regex over the tool name, and is absent for events that have
 * no tool (`SessionStart`, `UserPromptSubmit`, `SessionEnd`).
 */

export type HookEvent =
	| "PreToolUse"
	| "PostToolUse"
	| "UserPromptSubmit"
	| "SessionStart"
	| "SessionEnd"
	| "Stop"
	| "SubagentStop"
	| "Notification"
	| "PreCompact";

export interface HookCommand {
	type: "command";
	command: string;
	/** Seconds. Claude Code's default is 60. */
	timeout?: number;
}

export interface HookMatcher {
	/** Regex over the tool name. Absent or "*" matches everything. */
	matcher?: string;
	hooks: HookCommand[];
}

export type HookConfig = Partial<Record<HookEvent, HookMatcher[]>>;

/** Claude Code's default. A hook that hangs must not hang the session. */
export const DEFAULT_HOOK_TIMEOUT_SECONDS = 60;

/**
 * Does this matcher apply to this tool?
 *
 * Anchored: `"Bash"` must not match `"BashOutput"`. Claude Code's matchers are
 * regexes, and an unanchored one would fire the rtk rewrite on tools it was
 * never meant to touch.
 *
 * Case-insensitive for the same reason the permission rules are: settings are
 * written in Claude Code's casing (`Bash`, `Read`) while pi's tools are
 * lowercase. A rule that silently never fires is worse than no rule.
 */
export function matchesHook(matcher: string | undefined, toolName: string | undefined): boolean {
	if (!matcher || matcher === "*") return true;
	if (toolName === undefined) return false;
	try {
		return new RegExp(`^(?:${matcher})$`, "i").test(toolName);
	} catch {
		// A malformed regex must not take down every tool call. Treat it as
		// "does not match" rather than "matches everything" - failing closed on
		// a rewrite hook is the safe direction.
		return false;
	}
}

/** Every hook command registered for an event that applies to this tool. */
export function hooksFor(config: HookConfig, event: HookEvent, toolName?: string): HookCommand[] {
	const groups = config[event] ?? [];
	return groups
		.filter((g) => matchesHook(g.matcher, toolName))
		.flatMap((g) => g.hooks ?? [])
		.filter((h) => h?.type === "command" && typeof h.command === "string" && h.command.length > 0);
}

/**
 * Load and merge hooks across settings scopes.
 *
 * Hooks **accumulate** rather than override, which is the opposite of how the
 * `model` field merges and is deliberate: a project that defines a formatting
 * hook should not silently disable the user's global audit hook. Both run.
 */
export async function loadHooks(cwd: string): Promise<HookConfig> {
	const merged: HookConfig = {};
	for (const { path } of settingsFiles(cwd)) {
		let raw: { hooks?: HookConfig };
		try {
			raw = JSON.parse(await readFile(path, "utf8")) as { hooks?: HookConfig };
		} catch {
			// Missing is the common case; malformed is the user's business and is
			// already reported by the permissions loader reading the same file.
			continue;
		}
		for (const [event, groups] of Object.entries(raw.hooks ?? {})) {
			if (!Array.isArray(groups)) continue;
			const key = event as HookEvent;
			merged[key] = [...(merged[key] ?? []), ...groups];
		}
	}
	return merged;
}
