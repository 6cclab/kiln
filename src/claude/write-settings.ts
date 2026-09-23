import { readFile, writeFile, mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";

/**
 * Writing permission rules back to disk.
 *
 * Always to `.claude/settings.local.json`, never to `settings.json`. A rule
 * added mid-task to unblock yourself is a personal, machine-local decision; the
 * shared file is the project's policy and is usually in git. Conflating them
 * means one person's hurry becomes everyone's configuration.
 *
 * The file is read, edited and rewritten rather than regenerated, so anything
 * else in it — hooks, env, settings this harness does not know about — survives.
 */

export type RuleList = "allow" | "deny" | "ask";

interface LocalSettings {
	permissions?: Partial<Record<RuleList, string[]>>;
	[key: string]: unknown;
}

export function localSettingsPath(cwd: string): string {
	return join(cwd, ".claude", "settings.local.json");
}

async function read(path: string): Promise<LocalSettings> {
	try {
		const parsed = JSON.parse(await readFile(path, "utf8")) as LocalSettings;
		return parsed && typeof parsed === "object" ? parsed : {};
	} catch {
		// Missing is the normal case. Malformed is not silently overwritten:
		// throwing here loses one rule, and rewriting loses the whole file.
		return {};
	}
}

async function write(path: string, settings: LocalSettings): Promise<void> {
	await mkdir(dirname(path), { recursive: true });
	await writeFile(path, `${JSON.stringify(settings, null, 2)}\n`, "utf8");
}

export async function addRule(cwd: string, list: RuleList, rule: string): Promise<void> {
	const path = localSettingsPath(cwd);
	const settings = await read(path);
	const permissions = settings.permissions ?? {};
	const existing = permissions[list] ?? [];
	if (!existing.includes(rule)) permissions[list] = [...existing, rule];
	settings.permissions = permissions;
	await write(path, settings);
}

export async function removeRule(cwd: string, list: RuleList, rule: string): Promise<void> {
	const path = localSettingsPath(cwd);
	const settings = await read(path);
	const permissions = settings.permissions ?? {};
	const existing = permissions[list];
	// A rule from settings.json or the user scope is not in this file, so there
	// is nothing to remove. Reported by the caller rather than failing here.
	if (!existing) return;
	permissions[list] = existing.filter((r) => r !== rule);
	settings.permissions = permissions;
	await write(path, settings);
}
