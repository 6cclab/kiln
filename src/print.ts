import { BACKGROUND_CONTEXT } from "@earendil-works/pi-agent-core";
import type { StartedSession } from "./agent/session.ts";
import type { PermissionGate } from "./claude/permission.ts";

/**
 * Print mode (`-p`): run one prompt, emit the result, exit.
 *
 * Mirrors Claude Code's `--print`. Three things it buys:
 *
 *   - **Pipes and scripts.** `harness -p "summarize the diff" | pbcopy`.
 *   - **Testability.** The interactive TUI needs a TTY, so print mode is the
 *     only way to exercise the real loop from a non-interactive shell.
 *   - **CI.** A run that cannot prompt must still behave predictably.
 *
 * Permission handling is the important difference from interactive mode: there
 * is nobody to ask, so the gate refuses anything requiring confirmation rather
 * than proceeding. That is why `--permission-mode` matters here more than it
 * does interactively.
 */

export type OutputFormat = "text" | "json" | "stream-json";

export interface PrintOptions {
	session: StartedSession;
	prompt: string;
	format?: OutputFormat;
	/** Images from `@file.png` mentions, sent alongside the prompt. */
	images?: Array<{ type: "image"; data: string; mimeType: string }>;
	gate?: PermissionGate;
	/** Emit tool calls to stderr as they happen, so a long run shows progress. */
	verbose?: boolean;
	/**
	 * Newline-delimited JSON on stdout, one object per event, as it happens.
	 *
	 * The difference from `json` is not the encoding but the timing: `json`
	 * arrives once at the end, which is useless for driving a UI or a pipeline
	 * that wants to react while the run is still going.
	 */
	stream?: (event: StreamEvent) => void;
}

export type StreamEvent =
	| { type: "tool_start"; name: string; arg?: string }
	| { type: "tool_end"; name: string; isError: boolean }
	| { type: "assistant"; text: string }
	| { type: "result"; ok: boolean; text: string; blocked: string[] };

export interface PrintResult {
	text: string;
	toolCalls: Array<{ name: string; arg?: string; blocked?: boolean }>;
	blocked: string[];
	ok: boolean;
}

export async function runPrint(opts: PrintOptions): Promise<PrintResult> {
	const { session, prompt } = opts;

	const toolCalls: PrintResult["toolCalls"] = [];
	const blocked: string[] = [];
	let text = "";

	session.harness.events.on("tool_start", (event) => {
		const e = event as { toolName: string; args?: Record<string, unknown> };
		const arg = typeof e.args?.command === "string" ? e.args.command : (e.args?.path as string | undefined);
		toolCalls.push({ name: e.toolName, arg });
		// stderr, not stdout: progress must not contaminate piped output.
		if (opts.verbose) process.stderr.write(`· ${e.toolName}(${arg ?? ""})\n`);
		opts.stream?.({ type: "tool_start", name: e.toolName, arg });
	});

	session.harness.events.on("tool_end", (event) => {
		const e = event as { toolName: string; isError?: boolean };
		opts.stream?.({ type: "tool_end", name: e.toolName, isError: e.isError === true });
	});

	session.harness.events.on("message_end", (event) => {
		const message = (event as { message?: { role?: string; content?: unknown[] } }).message;
		if (message?.role !== "assistant") return;
		const chunk = (message.content ?? [])
			.filter((b): b is { type: "text"; text: string } => (b as { type?: string })?.type === "text")
			.map((b) => b.text)
			.join("");
		if (chunk) {
			text += `${chunk}\n`;
			opts.stream?.({ type: "assistant", text: chunk });
		}
	});

	const result = await session.lane.prompt(prompt, opts.images?.length ? (opts.images as never) : undefined, BACKGROUND_CONTEXT);
	blocked.push(...(opts.gate?.getBlocked() ?? []));
	const ok = result.ok && (result.value as { status?: string }).status === "completed";

	const printed: PrintResult = { text: text.trim(), toolCalls, blocked, ok };
	opts.stream?.({ type: "result", ok, text: printed.text, blocked });
	return printed;
}

export function formatPrintResult(result: PrintResult, format: OutputFormat): string {
	// stream-json already emitted everything as it happened; printing the result
	// again at the end would duplicate it.
	if (format === "stream-json") return "";
	if (format === "json") {
		return JSON.stringify(
			{
				ok: result.ok,
				text: result.text,
				toolCalls: result.toolCalls,
				blocked: result.blocked,
			},
			null,
			2,
		);
	}
	return result.text;
}
