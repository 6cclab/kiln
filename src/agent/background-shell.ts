import { BACKGROUND_CONTEXT, withCancel } from "@earendil-works/pi-agent-core";
import type { AgentHarnessTool, ExecutionEnv, ShellOutputUpdate } from "@earendil-works/pi-agent-core";
import type { TSchema } from "@earendil-works/pi-ai";

/**
 * Background shells.
 *
 * A dev server, a watch build, a long test run: commands that are useful
 * *while* they run and never "finish". Run in the foreground they occupy the
 * turn until they time out; the model waits for output that only arrives when
 * the process dies.
 *
 * So a background shell is started, given an id, and left alone. The model
 * polls it when it wants to, kills it when it is done, and spends nothing on it
 * in between.
 *
 * ## Why the output is a ring buffer
 *
 * A watch build emits output forever. Keeping all of it is a memory leak with
 * extra steps, and the last hundred lines are the only ones anyone reads. The
 * buffer is bounded and drops from the front, which is also what `tail -f`
 * does — the behavior people already expect from a log.
 *
 * ## Why reads are incremental
 *
 * `bash_output` returns only what is new since the last read. Returning the
 * whole buffer each time would put the same lines into context on every poll,
 * which on a 32k window is a way to end the conversation by checking on a
 * build. The cursor is per-shell and advances on read.
 */

/** Bounded so a watch process cannot grow the buffer without limit. */
const MAX_LINES = 500;

export type ShellStatus = "running" | "exited" | "killed" | "failed";

export interface BackgroundShell {
	id: string;
	command: string;
	status: ShellStatus;
	exitCode?: number;
	startedAt: number;
	endedAt?: number;
	/** Lines lost off the front of the buffer, so a truncated log says so. */
	dropped: number;
}

interface Entry extends BackgroundShell {
	/** The current output window, maintained per the update protocol. */
	text: string;
	lines: string[];
	/** Index into the total line stream, not into `lines`. */
	cursor: number;
	/** Lines that fell off the front, so a reader can be told what it missed. */
	droppedLines: number;
	cancel: () => void;
}

/** Split without the empty trailing element a final newline produces. */
function splitLines(text: string): string[] {
	if (!text) return [];
	const out = text.split("\n");
	if (out[out.length - 1] === "") out.pop();
	return out;
}

function countLines(text: string): number {
	return splitLines(text).length;
}

export class BackgroundShells {
	private shells = new Map<string, Entry>();
	private nextId = 1;

	/**
	 * Start a command and return immediately.
	 *
	 * The promise from `exec` is deliberately not awaited — that is the whole
	 * point — but it IS attached to, so an exit updates the record rather than
	 * leaving a dead shell reported as running forever.
	 */
	start(command: string, env: ExecutionEnv): BackgroundShell {
		const id = `bash_${this.nextId++}`;
		const { context, cancel } = withCancel(BACKGROUND_CONTEXT);

		const entry: Entry = {
			id,
			command,
			status: "running",
			startedAt: Date.now(),
			dropped: 0,
			text: "",
			lines: [],
			cursor: 0,
			droppedLines: 0,
			cancel,
		};
		this.shells.set(id, entry);

		void env
			.exec(
				command,
				{
					// `onUpdate` is how output is delivered; without it the output is
					// discarded and the shell reports a correct exit code having
					// produced nothing.
					onUpdate: (update) => this.absorb(entry, update),
				},
				context,
			)
			.then((result) => {
				entry.endedAt = Date.now();
				if (entry.status === "killed") return;
				if (result.ok) {
					entry.status = "exited";
					entry.exitCode = (result.value as { exitCode?: number }).exitCode;
				} else {
					entry.status = "failed";
				}
			})
			.catch(() => {
				entry.endedAt = Date.now();
				if (entry.status !== "killed") entry.status = "failed";
			});

		return this.snapshot(entry);
	}

	/**
	 * Absorb an output update.
	 *
	 * Four kinds, and they are NOT all appends — this was verified against a live
	 * `exec`, because getting it wrong duplicates or silently drops output:
	 *
	 *   - `replace` hands over the whole current window, under `output.text`
	 *     (not `text`, which is the append field).
	 *   - `append` adds `text` to the end.
	 *   - `slide` means the window scrolled: `drop` characters fell off the
	 *     front and `text` was added to the end. Appending without dropping
	 *     would duplicate the retained tail.
	 *   - `metadata` carries no output at all.
	 *
	 * Lines lost off the front are counted rather than forgotten, so an
	 * incremental reader can be told what it missed instead of quietly skipping.
	 */
	private absorb(entry: Entry, update: ShellOutputUpdate): void {
		switch (update.kind) {
			case "replace":
				entry.text = update.output.text;
				break;
			case "append":
				entry.text += update.text;
				break;
			case "slide": {
				const shed = entry.text.slice(0, update.drop);
				entry.droppedLines += countLines(shed);
				entry.text = entry.text.slice(update.drop) + update.text;
				break;
			}
			case "metadata":
				return;
		}

		// Cap here as well as at the source: `slide` only fires once the source
		// hits its own limit, and a command that never reaches it would otherwise
		// grow this buffer without bound.
		let lines = splitLines(entry.text);
		if (lines.length > MAX_LINES) {
			const excess = lines.length - MAX_LINES;
			lines = lines.slice(excess);
			entry.droppedLines += excess;
			entry.text = lines.join("\n");
		}
		entry.lines = lines;
	}

	/** Everything new since the last read, advancing the cursor. */
	read(id: string): { shell: BackgroundShell; lines: string[]; missed: number } | undefined {
		const entry = this.shells.get(id);
		if (!entry) return undefined;

		// `droppedLines` is the index of the first line still held, so anything
		// the cursor points at below that is gone. Say how much rather than
		// silently skipping it.
		const produced = entry.droppedLines + entry.lines.length;
		const missed = Math.max(0, entry.droppedLines - entry.cursor);
		const from = Math.max(0, entry.cursor - entry.droppedLines);
		const lines = entry.lines.slice(from);
		entry.cursor = produced;

		return { shell: this.snapshot(entry), lines, missed };
	}

	kill(id: string): BackgroundShell | undefined {
		const entry = this.shells.get(id);
		if (!entry) return undefined;
		if (entry.status === "running") {
			entry.status = "killed";
			entry.endedAt = Date.now();
			entry.cancel();
		}
		return this.snapshot(entry);
	}

	list(): BackgroundShell[] {
		return [...this.shells.values()].map((e) => this.snapshot(e));
	}

	get(id: string): BackgroundShell | undefined {
		const entry = this.shells.get(id);
		return entry ? this.snapshot(entry) : undefined;
	}

	/** Kill everything still running. Called on exit so nothing is orphaned. */
	killAll(): void {
		for (const entry of this.shells.values()) {
			if (entry.status === "running") {
				entry.status = "killed";
				entry.cancel();
			}
		}
	}

	private snapshot(entry: Entry): BackgroundShell {
		const { text: _t, lines: _l, cursor: _c, droppedLines, cancel: _k, ...rest } = entry;
		return { ...rest, dropped: droppedLines };
	}
}

function describe(shell: BackgroundShell): string {
	const seconds = Math.round(((shell.endedAt ?? Date.now()) - shell.startedAt) / 1000);
	const code = shell.exitCode === undefined ? "" : ` (exit ${shell.exitCode})`;
	return `${shell.id}  ${shell.status}${code}  ${seconds}s  ${shell.command}`;
}

export function createBashOutputTool<TContext extends object | undefined>(
	shells: BackgroundShells,
): AgentHarnessTool<TContext> {
	return {
		name: "bash_output",
		label: "Read background shell",
		description:
			"Read new output from a background shell started with run_in_background. " +
			"Returns only what has appeared since your last read, so polling is cheap.",
		parameters: {
			type: "object",
			properties: { id: { type: "string", description: "The shell id, e.g. bash_1." } },
			required: ["id"],
		} as unknown as TSchema,
		execute: async (_id: string, params: unknown) => {
			const id = String((params as { id?: string }).id ?? "").trim();
			const result = shells.read(id);
			if (!result) {
				const known = shells.list().map((s) => s.id);
				return {
					content: [
						{ type: "text" as const, text: `No shell "${id}". Running or finished: ${known.join(", ") || "none"}.` },
					],
					details: undefined,
				};
			}

			const header = describe(result.shell);
			const missed =
				result.missed > 0 ? `\n[${result.missed} earlier lines dropped - the buffer holds the last ${MAX_LINES}]` : "";
			const body = result.lines.length > 0 ? result.lines.join("\n") : "(no new output)";
			return { content: [{ type: "text" as const, text: `${header}${missed}\n\n${body}` }], details: undefined };
		},
	} as unknown as AgentHarnessTool<TContext>;
}

export function createKillShellTool<TContext extends object | undefined>(
	shells: BackgroundShells,
): AgentHarnessTool<TContext> {
	return {
		name: "kill_shell",
		label: "Kill background shell",
		description: "Stop a background shell. Do this when you are finished with a dev server or watch process.",
		parameters: {
			type: "object",
			properties: { id: { type: "string", description: "The shell id, e.g. bash_1." } },
			required: ["id"],
		} as unknown as TSchema,
		execute: async (_id: string, params: unknown) => {
			const id = String((params as { id?: string }).id ?? "").trim();
			const before = shells.get(id);
			const shell = shells.kill(id);
			if (!shell) return { content: [{ type: "text" as const, text: `No shell "${id}".` }], details: undefined };
			// "Killed" a shell that had already exited is a small lie that sends
			// the model looking for a cause it will not find.
			const verb = before?.status === "running" ? "Killed" : "Already finished:";
			return { content: [{ type: "text" as const, text: `${verb} ${describe(shell)}` }], details: undefined };
		},
	} as unknown as AgentHarnessTool<TContext>;
}

/**
 * `run_in_background` for bash.
 *
 * A separate tool rather than a flag on the existing one. pi's `bash` tool owns
 * its own schema and execution path, and adding a parameter to it would mean
 * wrapping and re-implementing that path — which is how the foreground case
 * quietly breaks. A distinct tool leaves `bash` untouched.
 */
export function createBackgroundBashTool<TContext extends { env: ExecutionEnv }>(
	shells: BackgroundShells,
): AgentHarnessTool<TContext> {
	return {
		name: "bash_background",
		label: "Background command",
		description:
			"Start a long-running command in the background and return immediately with a shell id. " +
			"Use for dev servers, watch builds, and anything that does not exit on its own. " +
			"Read its output with bash_output and stop it with kill_shell. " +
			"For a command that finishes, use bash instead - this one never returns its output directly.",
		parameters: {
			type: "object",
			properties: { command: { type: "string", description: "The command to run." } },
			required: ["command"],
		} as unknown as TSchema,
		// Signature is (toolCallId, params, onUpdate, toolContext, invocation,
		// context) - the tool context is the FOURTH argument. Reading the third
		// gets `onUpdate`, and the failure is a runtime "cannot read properties
		// of undefined" rather than a type error, because the parameter is typed
		// by position.
		execute: async (_id: string, params: unknown, _onUpdate: unknown, toolContext: TContext) => {
			const command = String((params as { command?: string }).command ?? "").trim();
			if (!command) {
				return { content: [{ type: "text" as const, text: "No command given." }], details: undefined };
			}
			const shell = shells.start(command, toolContext.env);
			return {
				content: [
					{
						type: "text" as const,
						text: `Started ${shell.id}: ${command}\nRead it with bash_output({id: "${shell.id}"}).`,
					},
				],
				details: undefined,
			};
		},
	} as unknown as AgentHarnessTool<TContext>;
}

/** `/bashes` — what is running, for the user rather than the model. */
export function renderShellList(shells: BackgroundShell[]): string {
	if (shells.length === 0) return "No background shells.";
	const running = shells.filter((s) => s.status === "running").length;
	return [`${shells.length} shell(s), ${running} running`, "", ...shells.map((s) => `  ${describe(s)}`)].join("\n");
}
