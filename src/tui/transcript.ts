import { dim, g, gray, green, red, strike, bold, italic, userBlock } from "./theme.ts";
import { visibleWidth, wrapTextWithAnsi } from "@earendil-works/pi-tui";

/**
 * Transcript rendering — the layout defined in docs/claude-code-parity.md §4a.
 *
 * Pure functions from data to lines. Nothing here touches a terminal, which is
 * what lets the layout be asserted in tests rather than eyeballed, and is where
 * parity is actually won or lost.
 *
 * The rules that matter, each of which is easy to get subtly wrong:
 *
 *   - Tool calls get a flush-left marker. Assistant prose gets none. Marking
 *     both makes the transcript unreadable.
 *   - Results indent two spaces, then the glyph, then two more. The glyph
 *     appears on the FIRST result line only; continuation lines align under the
 *     content, not under the glyph.
 *   - A call line names one identifying argument, not a serialized object.
 *   - Results are summarized, never dumped.
 */

/** Two spaces, glyph, two spaces -> content starts at column 5. */
const RESULT_INDENT = "  ";
const CONTINUATION_INDENT = "     ";

export type CallStatus = "running" | "ok" | "error";

export interface ToolCallView {
	name: string;
	/** The one identifying argument, already stringified. */
	primaryArg?: string;
	status: CallStatus;
	/** Summary line(s). Already truncated by the caller to the tier's budget. */
	resultLines?: string[];
	/** Total lines available, when more exist than are shown. */
	totalLines?: number;
}

function markerColor(status: CallStatus): (s: string) => string {
	if (status === "error") return red;
	if (status === "running") return dim;
	return green;
}

/**
 * Render a tool call and its result.
 *
 * ```
 * ⏺ Read(src/provider/ollama.ts)
 *   ⎿  Read 240 lines (ctrl+r to expand)
 * ```
 */
export function renderToolCall(view: ToolCallView): string[] {
	const glyphs = g();
	const head = `${markerColor(view.status)(glyphs.call)} ${bold(view.name)}(${dim(view.primaryArg ?? "")})`;
	const lines = [head];

	const body = view.resultLines ?? [];
	if (body.length === 0) return lines;

	// Glyph on the first result line only; the rest align under its content.
	lines.push(`${RESULT_INDENT}${gray(glyphs.result)}  ${body[0]}`);
	for (const extra of body.slice(1)) lines.push(`${CONTINUATION_INDENT}${extra}`);

	if (view.totalLines !== undefined && view.totalLines > body.length) {
		lines.push(`${CONTINUATION_INDENT}${dim(`… +${view.totalLines - body.length} lines (ctrl+r to expand)`)}`);
	}
	return lines;
}

export interface TodoView {
	content: string;
	status: "pending" | "in_progress" | "completed";
}

/**
 * Render a todo list under a tool-call header.
 *
 * Completed items are struck through and dimmed; the in-progress item is
 * distinct from both, because "which one is happening now" is the only question
 * the list has to answer at a glance.
 */
export function renderTodos(todos: TodoView[]): string[] {
	const glyphs = g();
	const lines = [`${green(glyphs.call)} ${bold("Update Todos")}`];

	todos.forEach((todo, i) => {
		const prefix = i === 0 ? `${RESULT_INDENT}${gray(glyphs.result)}  ` : CONTINUATION_INDENT;
		if (todo.status === "completed") {
			lines.push(`${prefix}${dim(glyphs.todoDone)} ${dim(strike(todo.content))}`);
		} else if (todo.status === "in_progress") {
			lines.push(`${prefix}${glyphs.todoActive} ${bold(todo.content)}`);
		} else {
			lines.push(`${prefix}${glyphs.todoPending} ${todo.content}`);
		}
	});
	return lines;
}

/**
 * Render a unified diff.
 *
 * Line numbers come from the new file for additions and the old file for
 * deletions, so a reader can navigate to what they are looking at. Colouring
 * the whole line rather than just the marker is what makes a diff scannable at
 * speed.
 */
export function renderDiff(patch: string, startLine = 1): string[] {
	const out: string[] = [];
	let oldNo = startLine;
	let newNo = startLine;

	for (const line of patch.split("\n")) {
		if (line.startsWith("@@")) {
			// Re-anchor on a hunk header so line numbers stay truthful across gaps.
			const m = line.match(/@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
			if (m) {
				oldNo = Number(m[1]);
				newNo = Number(m[2]);
			}
			out.push(dim(line));
			continue;
		}
		if (line.startsWith("+")) {
			out.push(green(`${String(newNo++).padStart(5)} + ${line.slice(1)}`));
		} else if (line.startsWith("-")) {
			out.push(red(`${String(oldNo++).padStart(5)} - ${line.slice(1)}`));
		} else {
			out.push(dim(`${String(newNo).padStart(5)}   ${line.slice(1)}`));
			oldNo++;
			newNo++;
		}
	}
	return out;
}

/**
 * Working indicator: `⠋ Pondering… (12s · 3.4k tokens · esc to interrupt)`
 *
 * On a local model a turn runs past two minutes, so this is load-bearing rather
 * than decorative — a still screen reads as a hang. The token count must come
 * from the live stream, not from the final usage record.
 */
export function renderSpinner(args: {
	frame: number;
	label: string;
	elapsedSeconds: number;
	tokens?: number;
}): string {
	const glyphs = g();
	const spin = glyphs.spinner[args.frame % glyphs.spinner.length];
	const parts = [`${args.elapsedSeconds}s`];
	if (args.tokens !== undefined) parts.push(`${formatTokens(args.tokens)} tokens`);
	parts.push("esc to interrupt");
	return `${green(spin)} ${args.label} ${dim(`(${parts.join(" · ")})`)}`;
}

export function formatTokens(n: number): string {
	if (n < 1000) return String(n);
	return `${(n / 1000).toFixed(1)}k`;
}

/**
 * Gerunds for the spinner.
 *
 * Deliberately this project's own vocabulary rather than Claude Code's word
 * list: the *shape* is the parity requirement, the specific words are flavor,
 * and copying someone's jokes is not parity.
 */
const LABELS = [
	"Thinking",
	"Working",
	"Pondering",
	"Chewing",
	"Considering",
	"Noodling",
	"Mulling",
	"Digging",
	"Untangling",
	"Reckoning",
];

export function pickLabel(seed: number): string {
	return LABELS[seed % LABELS.length];
}

/** Errors are red and visually distinct from ordinary tool output. */
export function renderError(message: string): string[] {
	return message.split("\n").map((line) => red(line));
}

export interface ThinkingView {
	text: string;
	/** Still streaming. The label reads as present tense until it finishes. */
	active?: boolean;
	expanded?: boolean;
}

/**
 * Render a reasoning block.
 *
 * ```
 * ✻ Thinking… (14 lines, ctrl+r to expand)
 * ```
 *
 * Collapsed by default, per the parity spec. That is not only a visual
 * preference here: the models this harness targets are reasoning models, and
 * qwen3.8 emits a block of reasoning before most answers. Shown in full it
 * buries the answer under its own working, every turn.
 *
 * Dimmed italic when expanded, so it never competes with the assistant's actual
 * prose — the reader should be able to skip it without deciding to.
 */
export function renderThinking(view: ThinkingView): string[] {
	const glyphs = g();
	const body = view.text.trim();
	if (!body) return [];

	const lines = body.split("\n");
	// Present tense while streaming: "Thinking…" reads as something happening,
	// "Thought" as something finished.
	const label = view.active ? "Thinking…" : "Thought";

	if (!view.expanded) {
		const count = `${lines.length} line${lines.length === 1 ? "" : "s"}`;
		const hint = view.active ? count : `${count}, ctrl+r to expand`;
		return [`${dim(glyphs.thinking)} ${dim(label)} ${dim(`(${hint})`)}`];
	}

	return [
		`${dim(glyphs.thinking)} ${dim(label)}`,
		...lines.map((line) => `${RESULT_INDENT}${dim(italic(line))}`),
	];
}

/**
 * The user's own message.
 *
 * Rendered as a filled block spanning the width rather than a `> ` prefix.
 * Scrolling back through a long session, the question you asked is the landmark
 * you navigate by — a two-character prefix does not survive that at a glance,
 * and a coloured one competes with the tool markers and diffs that already use
 * colour to mean something.
 *
 * Padded to the full width so the fill reads as a band rather than a ragged
 * highlight that ends wherever the text happened to stop.
 */
export function renderUserMessage(text: string, width: number): string[] {
	const glyphs = g();
	const inner = Math.max(1, width - 2);
	const wrapped = text.split("\n").flatMap((line) => wrapTextWithAnsi(line, inner));
	return wrapped.map((line) => {
		const pad = " ".repeat(Math.max(0, inner - visibleWidth(line)));
		return userBlock(`${dim(glyphs.userMark)} ${line}${pad}`);
	});
}

export interface TurnSummary {
	seconds: number;
	tokens?: number;
	toolCalls?: number;
}

/**
 * The line that closes a turn.
 *
 * Without it a finished turn just stops, and the transcript gives no sense of
 * what a request cost — which on a local model at ~20 tok/s is the number you
 * are actually budgeting against. Elapsed time is the honest headline there;
 * tokens and tool calls explain it.
 */
export function renderTurnSummary(summary: TurnSummary): string[] {
	const glyphs = g();
	const parts = [`${summary.seconds}s`];
	if (summary.toolCalls) parts.push(`${summary.toolCalls} tool call${summary.toolCalls === 1 ? "" : "s"}`);
	if (summary.tokens) parts.push(`${formatTokens(summary.tokens)} tokens`);
	return [dim(`${glyphs.summary} Worked for ${parts.join(" · ")}`)];
}
