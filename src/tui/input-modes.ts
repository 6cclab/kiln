import { appendFile, readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import type { ExecutionEnv } from "@earendil-works/pi-agent-core";
import { BACKGROUND_CONTEXT } from "@earendil-works/pi-agent-core";
import { dim, green, red } from "./theme.ts";

/**
 * The `!` and `#` input prefixes (parity spec §2).
 *
 * `/` and `@` are handled by pi-tui's autocomplete provider. These two are
 * different in kind: they are not completions but *modes* — the line is acted
 * on directly instead of being sent to the model.
 *
 * That distinction is the point of both. `!ls` should not cost a model turn,
 * and on a local model at ~21 tok/s the difference is a second versus a minute.
 */

export interface ModeResult {
	/** Lines to show in the transcript. */
	output: string[];
	/** Text to send to the model, when the mode produces one. */
	prompt?: string;
}

/**
 * `!command` — run a shell command directly.
 *
 * The output goes into the transcript so it is visible, but it is NOT sent to
 * the model: this is the user's own shell, not a tool call. If they want the
 * model to see the result, they can reference it in the next message.
 */
export async function runBang(command: string, env: ExecutionEnv): Promise<ModeResult> {
	// `exec` returns only metadata (exit code, truncation). Output is delivered
	// through `onUpdate`, and is DISCARDED when neither `onUpdate` nor `capture`
	// is supplied — which is easy to miss, since the call still succeeds and
	// reports a correct exit code while silently producing nothing.
	let text = "";
	const result = await env.exec(
		command,
		{
			// Bound it: `!yes` or a runaway build must not fill memory.
			capture: { limits: { maxBytes: 64 * 1024, maxLines: 2000 } },
			onUpdate: (update) => {
				switch (update.kind) {
					case "replace":
						text = update.output.text;
						break;
					case "append":
						text += update.text;
						break;
					case "slide":
						// The window scrolled: older output was dropped from the front.
						// Appending without dropping would duplicate the retained tail.
						text = text.slice(update.drop) + update.text;
						break;
					case "metadata":
						break;
				}
			},
		},
		BACKGROUND_CONTEXT,
	);

	if (!result.ok) {
		return { output: [red(`! ${command}`), red(`  ${result.error.message}`)] };
	}

	const { exitCode } = result.value;
	const lines = [`${dim("!")} ${command}`];
	const body = text.trimEnd();

	if (body) lines.push(...body.split("\n").map((l) => `  ${l}`));
	// A silent success is ambiguous — say so rather than leaving a bare prompt.
	else lines.push(dim("  (no output)"));

	if (exitCode !== undefined && exitCode !== 0) lines.push(red(`  exit ${exitCode}`));
	return { output: lines };
}

/**
 * `#note` — append a line to memory.
 *
 * Writes to the project `CLAUDE.md` when one exists, otherwise the user's.
 * Choosing the project file first matters: most notes worth keeping are about
 * the code in front of you, not about you.
 *
 * Takes effect on the next start, since memory is assembled into the system
 * prompt at session creation — and saying so beats letting the user wonder why
 * the model did not react.
 */
export async function addMemory(note: string, cwd: string): Promise<ModeResult> {
	const projectFile = join(cwd, "CLAUDE.md");
	const userFile = join(homedir(), ".claude", "CLAUDE.md");

	let target = userFile;
	try {
		await readFile(projectFile, "utf8");
		target = projectFile;
	} catch {
		// No project memory; fall back to the user's.
	}

	try {
		await appendFile(target, `\n${note.trim()}\n`);
		return { output: [`${green("#")} added to ${target}`, dim("  applies from the next session")] };
	} catch (err) {
		return { output: [red(`# could not write ${target}: ${(err as Error).message}`)] };
	}
}

/** Classify a submitted line. `undefined` means "send it to the model". */
export function classifyInput(line: string): { mode: "bang" | "memory"; body: string } | undefined {
	if (line.startsWith("!") && line.length > 1) return { mode: "bang", body: line.slice(1).trim() };
	if (line.startsWith("#") && line.length > 1) return { mode: "memory", body: line.slice(1).trim() };
	return undefined;
}
