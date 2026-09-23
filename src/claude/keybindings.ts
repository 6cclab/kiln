import { readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import { KeybindingsManager, setKeybindings, TUI_KEYBINDINGS } from "@earendil-works/pi-tui";

/**
 * `~/.claude/keybindings.json` (parity spec §6).
 *
 * pi-tui already has the machinery: a `KeybindingsManager` with user overrides
 * and conflict detection, reached through a module-level `setKeybindings`. All
 * that was missing was reading the file Claude Code reads.
 *
 * Conflicts are **reported, not resolved**. A user who has bound two actions to
 * the same key has made a mistake only they can settle, and silently dropping
 * one of them produces a key that works for a while and then does something
 * else. Reporting costs one line at startup and saves an afternoon.
 */

export interface KeybindingsResult {
	/** Whether a user file was found and applied. */
	loaded: boolean;
	path: string;
	/** Human-readable conflict descriptions, for the caller to surface. */
	conflicts: string[];
	/** Set when the file existed but could not be used. */
	error?: string;
}

export function keybindingsPath(): string {
	return join(homedir(), ".claude", "keybindings.json");
}

export async function loadKeybindings(path = keybindingsPath()): Promise<KeybindingsResult> {
	const result: KeybindingsResult = { loaded: false, path, conflicts: [] };

	let raw: string;
	try {
		raw = await readFile(path, "utf8");
	} catch {
		// Absent is the normal case, not an error. Defaults still apply.
		return result;
	}

	let config: unknown;
	try {
		config = JSON.parse(raw);
	} catch (err) {
		// The file exists and is broken. Say so: falling back to defaults without
		// a word looks identical to the overrides simply not working.
		result.error = `could not parse ${path}: ${(err as Error).message}`;
		return result;
	}

	// `typeof [] === "object"`, so an array slips past a bare typeof check and is
	// then accepted as a config that binds nothing.
	if (!config || typeof config !== "object" || Array.isArray(config)) {
		result.error = `${path} must contain a JSON object mapping actions to keys`;
		return result;
	}

	try {
		const manager = new KeybindingsManager(TUI_KEYBINDINGS, config as never);
		setKeybindings(manager);
		result.loaded = true;
		result.conflicts = manager
			.getConflicts()
			.map((c) => `${String(c.key)} is bound to ${c.keybindings.join(" and ")}`);
	} catch (err) {
		result.error = `could not apply ${path}: ${(err as Error).message}`;
	}

	return result;
}
