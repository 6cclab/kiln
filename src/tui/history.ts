import { appendFile, mkdir, readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join } from "node:path";

/**
 * Prompt history for the input line.
 *
 * pi-tui's editor navigates history with the arrow keys, but only over what it
 * has been given — and nothing was calling `addToHistory`, so up-arrow moved
 * through an empty list and looked like a dead key.
 *
 * Persisted, because the useful case is across sessions: the thing you want to
 * re-run is usually the long prompt you wrote yesterday, not the one two lines
 * up that is still on screen.
 *
 * Stored under `~/.harness`, not in the project. A prompt can contain anything
 * you typed, and putting that in a repo directory invites committing it.
 */

const MAX_ENTRIES = 500;

export function historyPath(): string {
	return join(homedir(), ".harness", "history");
}

/** Most recent last, which is the order the editor walks backwards through. */
export async function loadHistory(path = historyPath()): Promise<string[]> {
	try {
		const lines = (await readFile(path, "utf8"))
			.split("\n")
			// Entries are newline-escaped on write so a multi-line prompt stays
			// one entry; unescape on the way back.
			.map((line) => line.replace(/\\n/g, "\n").trim())
			.filter(Boolean);
		return lines.slice(-MAX_ENTRIES);
	} catch {
		// No history yet is the normal first run.
		return [];
	}
}

export async function appendHistory(entry: string, path = historyPath()): Promise<void> {
	const text = entry.trim();
	if (!text) return;
	try {
		await mkdir(dirname(path), { recursive: true });
		// Appended rather than rewritten: two sessions open at once should
		// interleave rather than one truncating the other's history.
		await appendFile(path, `${text.replace(/\n/g, "\\n")}\n`, "utf8");
	} catch {
		// History is a convenience. Failing to record it must never cost the
		// turn the user just submitted.
	}
}
