import {
	CombinedAutocompleteProvider,
	Editor,
	Markdown,
	ProcessTerminal,
	TuiMainScreen,
	type Component,
	type EditorTheme,
	type MarkdownTheme,
	type Focusable,
	type TUI,
	visibleWidth,
} from "@earendil-works/pi-tui";
import { fitLines, fitStatus } from "./width.ts";
import { describeMentions, resolveMentions } from "./mentions.ts";
import type { SubagentEvent } from "../agent/dispatch.ts";
import { BACKGROUND_CONTEXT, estimateTokens } from "@earendil-works/pi-agent-core";
import type { AgentMessage } from "@earendil-works/pi-agent-core";
import type { ImageContent } from "@earendil-works/pi-ai";
import type { StartedSession } from "../agent/session.ts";
import type { CommandRegistry } from "../commands/registry.ts";
import { PermissionPromptView } from "./permission-prompt.ts";
import type { PermissionGate } from "../claude/permission.ts";
import type { PermissionMode } from "../claude/settings.ts";
import type { TodoStore } from "../agent/todo.ts";
import type { PlanDecision } from "../agent/plan-mode.ts";
import { usableTokens } from "../budget/tier.ts";
import { dim, gray, green, bold, cyan, yellow, italic, strike, g, setPlainMode } from "./theme.ts";
import { highlightCode } from "./highlight.ts";
import { createKeyRouter } from "./keys.ts";
import { renderStatus, type StatusState } from "./status.ts";
import { readGitStatus } from "./git.ts";
import { addMemory, classifyInput, runBang } from "./input-modes.ts";
import {
	formatTokens,
	pickLabel,
	renderError,
	renderSpinner,
	renderToolCall,
	renderTodos,
	renderThinking,
	renderUserMessage,
	renderTurnSummary,
	type ThinkingView,
	type ToolCallView,
} from "./transcript.ts";

/**
 * The interactive shell.
 *
 * Renders into the **main screen**, not the alt screen. That is a parity
 * decision, not an implementation detail: Claude Code appends to normal
 * scrollback so the terminal's own scroll, search and copy keep working. An
 * alt-screen TUI takes the terminal hostage and loses all three.
 */

const SPINNER_INTERVAL_MS = 80;

/**
 * Markdown styling for assistant prose.
 *
 * Code blocks get a dimmed border and a two-space indent so they read as a
 * distinct block without a heavy frame competing with the tool-call markers.
 */
export const markdownTheme: MarkdownTheme = {
	heading: (t) => bold(t),
	link: (t) => cyan(t),
	linkUrl: (t) => dim(t),
	code: (t) => yellow(t),
	codeBlock: (t) => t,
	codeBlockBorder: (t) => dim(t),
	quote: (t) => dim(t),
	quoteBorder: (t) => gray(t),
	hr: (t) => dim(t),
	listBullet: (t) => green(t),
	bold: (t) => bold(t),
	italic: (t) => italic(t),
	strikethrough: (t) => strike(t),
	underline: (t) => t,
	highlightCode,
	codeBlockIndent: "  ",
};

const editorTheme: EditorTheme = {
	borderColor: (s) => gray(s),
	selectList: {
		selectedPrefix: (s) => green(s),
		selectedText: (s) => bold(s),
		description: (s) => dim(s),
		scrollInfo: (s) => dim(s),
		noMatch: (s) => dim(s),
	},
};

/**
 * Rounded border around the editor.
 *
 * pi-tui's `Editor` draws plain horizontal rules above and below itself, and
 * `Box` provides padding but no border, so neither gives Claude Code's
 * `╭─╮ │ ╰─╯` input box. This wraps the editor's own output.
 *
 * Two details this must not break:
 *   - `CURSOR_MARKER`. The TUI locates the hardware cursor by finding that
 *     marker in the rendered line. Prefixing each line shifts the marker's
 *     index along with the text, so the cursor stays correct — but the marker
 *     must be passed through untouched, never trimmed or re-measured.
 *   - Width. Lines are padded using `visibleWidth`, which ignores ANSI escapes;
 *     `String.length` would count them and misalign the right edge.
 */
export class BorderedEditor implements Component, Focusable {
	private editor: Editor;
	private color: (s: string) => string;

	constructor(editor: Editor, color: (s: string) => string) {
		this.editor = editor;
		this.color = color;
	}

	/**
	 * Forward focus to the editor.
	 *
	 * `Focusable` is just a boolean the TUI assigns on focus change, and the
	 * editor only emits `CURSOR_MARKER` while its own flag is true. Focusing the
	 * wrapper without forwarding left the editor permanently unfocused, so the
	 * hardware cursor had nothing to anchor to.
	 */
	get focused(): boolean {
		return (this.editor as unknown as Focusable).focused;
	}

	set focused(value: boolean) {
		(this.editor as unknown as Focusable).focused = value;
	}

	handleInput(data: string): void {
		this.editor.handleInput?.(data);
	}

	invalidate(): void {
		this.editor.invalidate();
	}

	render(width: number): string[] {
		// The editor ALWAYS renders its own horizontal rule as the first and last
		// line, and it draws scroll indicators into them ("─── ↑ 3 more ───")
		// when the content overflows. Those rules are kept and the rest of the
		// box is not.
		//
		// A full ╭─╮ │ ╰─╯ box costs two rows of chrome and boxes in text that is
		// already the only editable thing on screen. One rule above, a prompt
		// marker, and the status line below is lighter and gives a row back — on
		// a short terminal that row is a line of transcript.
		const body = this.editor.render(Math.max(1, width - 2));
		if (body.length < 2) return body;

		const marker = this.color(g().userMark);
		return [
			this.color(body[0]),
			// The prompt marker replaces the left border; the CURSOR_MARKER inside
			// the line shifts with it, which is what keeps the hardware cursor in
			// the right column.
			// No space after the marker: the editor's own paddingX already supplies
			// one, and adding another puts a two-column gap before the cursor.
			...body.slice(1, -1).map((line) => `${marker}${line}`),
		];
	}
}

/**
 * Completed output.
 *
 * Append-only: finished blocks never re-render, so the terminal's scrollback
 * holds a stable transcript and pi-tui only ever diffs the live region at the
 * bottom. Mutating history would make every frame a full redraw.
 */
type Block =
	| { kind: "lines"; lines: string[] }
	| { kind: "markdown"; md: Markdown }
	/** A tool call keeping its FULL output, so Ctrl+R can reveal it later. */
	| { kind: "tool"; view: ToolCallView; full: string[] }
	/** A reasoning block. Collapsed by default; Ctrl+R expands it with the rest. */
	| { kind: "thinking"; view: ThinkingView }
	/** The user's own message. Re-wraps on resize, so it keeps its own kind. */
	| { kind: "user"; text: string };

export class TranscriptView implements Component {
	// Blocks, not a flat line list, for two reasons: markdown must re-wrap when
	// the terminal is resized (impossible once flattened at one fixed width),
	// and tool output must survive truncation so Ctrl+R has something to expand.
	private blocks: Block[] = [];
	private expanded = false;

	append(lines: string[]): void {
		this.blocks.push({ kind: "lines", lines });
	}

	/** The user's own message, rendered as a filled block. */
	appendUser(text: string): void {
		this.blocks.push({ kind: "user", text });
	}

	appendMarkdown(text: string): void {
		this.blocks.push({ kind: "markdown", md: new Markdown(text, 0, 0, markdownTheme) });
	}

	/** Keep the full output; the collapsed view is derived at render time. */
	appendToolCall(view: ToolCallView, full: string[]): void {
		this.blocks.push({ kind: "tool", view, full });
	}

	/**
	 * Start (or continue) a reasoning block.
	 *
	 * Returns the view so the caller can append deltas to it in place. Streaming
	 * into one block rather than appending a new one per delta keeps the
	 * transcript from growing a line every few tokens.
	 */
	appendThinking(view: ThinkingView): void {
		this.blocks.push({ kind: "thinking", view });
	}

	appendBlank(): void {
		if (this.blocks.length > 0) this.blocks.push({ kind: "lines", lines: [""] });
	}

	clear(): void {
		this.blocks = [];
	}

	/** Ctrl+R. Returns the new state so the caller can report it. */
	toggleExpanded(): boolean {
		this.expanded = !this.expanded;
		return this.expanded;
	}

	isExpanded(): boolean {
		return this.expanded;
	}

	/** Whether anything is actually truncated, so Ctrl+R can be a no-op when not. */
	hasCollapsed(): boolean {
		return this.blocks.some((b) =>
			b.kind === "tool"
				? b.full.length > (b.view.resultLines?.length ?? 0)
				: b.kind === "thinking" && b.view.text.trim().length > 0,
		);
	}

	invalidate(): void {
		for (const block of this.blocks) if (block.kind === "markdown") block.md.invalidate();
	}

	render(width: number): string[] {
		return this.blocks.flatMap((block) => {
			if (block.kind === "lines") return fitLines(block.lines, width);
			if (block.kind === "user") return renderUserMessage(block.text, width);
			if (block.kind === "thinking") {
				return fitLines(renderThinking({ ...block.view, expanded: this.expanded }), width, "    ");
			}
			// Markdown already wraps to the width it is given.
			if (block.kind === "markdown") return block.md.render(width);
			// Expanded: show everything and drop the "+N lines" affordance, since
			// there is nothing left hidden to advertise.
			return fitLines(
				this.expanded
					? renderToolCall({ ...block.view, resultLines: block.full, totalLines: undefined })
					: renderToolCall(block.view),
				width,
			);
		});
	}
}

/**
 * The working indicator, rendered ABOVE the input box.
 *
 * Spinner and footer are separate components because they live on opposite
 * sides of the editor: the spinner belongs to the conversation flow above it,
 * the status line sits below it. Merging them into one slot put the footer in
 * the wrong place.
 */
export class SpinnerView implements Component {
	private busy = false;
	private frame = 0;
	private startedAt = 0;
	private tokens = 0;
	private label = "Working";
	/** The turn's own label, restored when a temporary one is cleared. */
	private baseLabel = "Working";

	start(seed: number): void {
		this.busy = true;
		this.frame = 0;
		this.tokens = 0;
		this.startedAt = Date.now();
		this.label = pickLabel(seed);
		this.baseLabel = this.label;
	}

	stop(): void {
		this.busy = false;
	}

	tick(): void {
		this.frame++;
	}

	/** A temporary label for a phase within the turn, e.g. reasoning. */
	setLabel(label: string): void {
		this.label = label;
	}

	/** Back to the gerund this turn started with. */
	resetLabel(): void {
		this.label = this.baseLabel;
	}

	/** Totals are cumulative for the run, so this replaces rather than adds. */
	setTokens(n: number): void {
		this.tokens = n;
	}

	getTokens(): number {
		return this.tokens;
	}

	isBusy(): boolean {
		return this.busy;
	}

	invalidate(): void {}

	render(width: number): string[] {
		if (!this.busy) return [];
		// Truncated, not wrapped: the spinner is one row by definition, and a
		// two-row spinner makes the whole transcript above it jump on each tick.
		return [
			fitStatus(
				renderSpinner({
					frame: this.frame,
					label: this.label,
					elapsedSeconds: Math.round((Date.now() - this.startedAt) / 1000),
					tokens: this.tokens || undefined,
				}),
				width,
			),
		];
	}
}

/** The status line, rendered BELOW the input box. */
/**
 * The status line, below the input box.
 *
 * Holds live state rather than a pre-rendered string: context use, spend and
 * git status all change while you work, and a `setText` interface meant every
 * caller had to remember to re-render the whole line whenever any one of them
 * moved. Several did not, which is how it ended up showing facts that never
 * changed all session.
 */
export class FooterView implements Component {
	private state: StatusState;
	/** A transient note appended to the first row, e.g. "expanded". */
	private note = "";

	constructor(state: StatusState) {
		this.state = state;
	}

	update(patch: Partial<StatusState>): void {
		this.state = { ...this.state, ...patch };
	}

	/** Shown until the next update clears it. */
	setNote(note: string): void {
		this.note = note;
	}

	invalidate(): void {}

	render(width: number): string[] {
		const [status, mode] = renderStatus(this.state);
		const first = this.note ? `${status}${dim(`  ·  ${this.note}`)}` : status;
		// Truncated, not wrapped: the status line is two rows by definition, and
		// a third row would push the input box around as the numbers change.
		return [fitStatus(first, width), fitStatus(mode, width)];
	}
}

/**
 * The one identifying argument for a call line.
 *
 * Claude Code shows `Read(src/foo.ts)`, not a serialized argument object. These
 * keys are tried in order of how well they identify the call.
 */
function primaryArg(args: unknown): string | undefined {
	if (typeof args === "string") return args;
	if (!args || typeof args !== "object") return undefined;
	const obj = args as Record<string, unknown>;
	for (const key of ["path", "file_path", "filePath", "command", "pattern", "query", "url"]) {
		const value = obj[key];
		if (typeof value === "string") return value;
	}
	return undefined;
}

/** `read` -> `Read`, matching Claude Code's title-cased tool names. */
function titleCase(name: string): string {
	return name.charAt(0).toUpperCase() + name.slice(1);
}

/** Tool results are summarized, never dumped. */
/**
 * Pull the displayable text out of a tool result.
 *
 * The important case is `content`, which is an ARRAY of blocks
 * (`[{type:"text", text:"..."}]`), not a string — that is the shape every tool
 * in pi returns. Treating it as a string falls through to `JSON.stringify`,
 * which renders the whole result as one escaped line:
 *
 *     {"content":[{"type":"text","text":"file line one\nfile line two\n..."}]}
 *
 * Unreadable, and worse for a 300-line result than the result itself. Blocks
 * that are not text (images, structured details) are skipped rather than
 * stringified, since the transcript has no way to show them inline anyway.
 */
export function summarize(result: unknown): string[] {
	if (result === undefined || result === null) return [];
	if (typeof result === "string") return lines(result);
	if (Array.isArray(result)) return blockText(result);

	const obj = result as Record<string, unknown>;

	// `output` and `text` are plain strings when present; `content` is the
	// block array. Checked in that order because a tool that sets both means
	// the string as the human-facing form.
	if (typeof obj.output === "string") return lines(obj.output);
	if (typeof obj.text === "string") return lines(obj.text);
	if (typeof obj.content === "string") return lines(obj.content);
	if (Array.isArray(obj.content)) return blockText(obj.content);

	// Last resort, and now genuinely a last resort rather than the common path.
	return JSON.stringify(result).split("\n");
}

/** Split, dropping the empty final element a trailing newline produces. */
function lines(text: string): string[] {
	const out = text.split("\n");
	// Command output almost always ends in a newline. Kept, it renders as a
	// blank row under every tool call.
	if (out.length > 1 && out[out.length - 1] === "") out.pop();
	return out;
}

function blockText(blocks: unknown[]): string[] {
	const text = blocks
		.map((b) => {
			if (typeof b === "string") return b;
			const block = b as { type?: string; text?: unknown };
			return block?.type === "text" && typeof block.text === "string" ? block.text : "";
		})
		.filter(Boolean)
		.join("\n");
	return text ? lines(text) : [];
}

export interface AppOptions {
	session: StartedSession;
	commands: CommandRegistry;
	modelLabel: string;
	cwd: string;
	/** Filesystem/shell env, for the `!` prefix. */
	env?: import("@earendil-works/pi-agent-core").ExecutionEnv;
	/** Permission gate. Its prompter is bound to this view once the TUI exists. */
	gate?: PermissionGate;
	/** Todo store. Updates render into the transcript as they happen. */
	todos?: TodoStore;
	/** Receives the plan-approval callback once this view exists. */
	onPlanApprover?: (fn: (plan: string) => Promise<PlanDecision>) => void;
	/** Receives the subagent progress sink once this view exists. */
	onSubagentEvents?: (fn: (e: SubagentEvent) => void) => void;
	/** Receives the hook-notice sink once this view exists. */
	onHookNotices?: (fn: (message: string) => void) => void;
	/**
	 * UserPromptSubmit hooks. Returns text to prepend to the turn, or a reason
	 * the turn was refused. Run per prompt, so it cannot be hoisted out.
	 */
	runPromptHooks?: (prompt: string) => Promise<{ context: string[]; blocked?: { reason: string } }>;
	/** Context from SessionStart hooks, prepended to the first turn only. */
	startupContext?: string[];
	/** Flat text, no color or animation. Mirrors Claude Code's --ax-screen-reader. */
	plain?: boolean;
}

export async function runApp(opts: AppOptions): Promise<void> {
	const { session, commands } = opts;
	if (opts.plain) setPlainMode(true);

	const tui: TUI = new TuiMainScreen(new ProcessTerminal());
	const transcript = new TranscriptView();
	const spinner = new SpinnerView();
	const footer = new FooterView({
		modelLabel: opts.modelLabel,
		contextWindow: session.tier.contextWindow,
		mode: opts.gate?.mode ?? "manual",
		startedAt: Date.now(),
	});
	const permission = new PermissionPromptView();
	// paddingX pads content lines only, not the editor's border rules, so the
	// box edges stay flush while the text gets breathing room.
	const editor = new Editor(tui, editorTheme, { paddingX: 1 });
	const framedEditor = new BorderedEditor(editor, gray);

	// One provider serves `/` commands and `@` file mentions, which is how
	// pi-tui models it and how Claude Code behaves.
	// Pass Command objects (bare `name`), NOT pre-slashed items: the provider
	// replaces a prefix that already includes the leading "/", so a value of
	// "/model" completed to "//model".
	editor.setAutocompleteProvider(new CombinedAutocompleteProvider(await commands.list(), opts.cwd));

	// Mutable: consumed by the first turn, then emptied.
	let startupContext = [...(opts.startupContext ?? [])];

	/** Clears any transient note and re-reads the mode, which Shift+Tab moves. */
	const refreshStatus = (note = "") => {
		footer.setNote(note);
		footer.update({ mode: opts.gate?.mode ?? "manual" });
	};

	// Git state is read once at startup and after each turn rather than on every
	// render: the status line redraws on every keystroke, and spawning `git` at
	// that rate would make typing feel heavy for a fact that changes rarely.
	void readGitStatus(opts.cwd).then((git) => {
		if (git) {
			footer.update({ git });
			tui.requestRender();
		}
	});

	// A subagent runs for minutes producing nothing on screen, which reads as a
	// hang. Its tool calls are NOT echoed - that would undo the context
	// isolation visually even though it is real underneath - but the dispatch,
	// the model it landed on, and the result size are.
	// Hook activity goes to the transcript as a dim aside. The user needs to know
	// their command was rewritten; the model does not.
	opts.onHookNotices?.((message) => {
		transcript.append([dim(`  hook: ${message}`)]);
		tui.requestRender();
	});

	opts.onSubagentEvents?.((e) => {
		if (e.kind === "start") {
			const note = e.inherited ? ` ${dim("(inherited; the requested model is not on this provider)")}` : "";
			transcript.append([`${dim("\u2514")} ${bold(e.agent)} ${dim(e.description)} ${dim("on")} ${e.modelId}${note}`]);
		} else if (e.kind === "done") {
			transcript.append([dim(`  ${e.agent} finished - ${e.toolCalls} tool calls, ${e.chars} chars returned`)]);
		} else if (e.kind === "error") {
			transcript.append(renderError(`${e.agent}: ${e.message}`));
		}
		tui.requestRender();
	});

	tui.addChild(transcript);
	// Order is the layout: transcript, spinner, input box, status line.
	tui.addChild(spinner);
	// Inline, above the input box - the conversation being asked about stays visible.
	tui.addChild(permission);
	tui.addChild(framedEditor);
	tui.addChild(footer);
	// Focus the wrapper: it forwards input, and its render carries the editor's
	// CURSOR_MARKER so the hardware cursor still lands correctly.
	tui.setFocus(framedEditor);

	// The gate is created before the TUI (the session needs it), so its prompter
	// is bound here rather than at construction.
	// The todo tool returns only a count to the model; the list itself is
	// rendered here, so the user sees it without it costing context.
	opts.todos?.onChange((items) => {
		transcript.appendBlank();
		transcript.append(renderTodos(items));
		tui.requestRender();
	});

	opts.onPlanApprover?.(async (plan) => {
		tui.requestRender();
		const decision = await permission.askPlan(plan);
		// Reflect an approved mode change immediately; the footer is how the user
		// sees that plan mode actually ended.
		if (decision.kind === "approve") refreshStatus();
		tui.requestRender();
		return decision;
	});

	opts.gate?.setPrompter(async (request) => {
		// Render immediately so the prompt appears before the await blocks on the
		// user; otherwise the frame showing the question never gets drawn.
		tui.requestRender();
		const choice = await permission.ask(request);
		tui.requestRender();
		return choice;
	});

	// --- live turn state -------------------------------------------------

	// Tool calls are keyed by id because `tool_start` and `tool_end` are separate
	// events and, with parallel execution, can interleave.
	const pending = new Map<string, ToolCallView>();

	// Ollama reports no usage until the run ends, so a 2-minute local turn would
	// show no token count at all - exactly when the user most needs to see
	// movement. Estimate from streamed text with pi's own estimator (the same one
	// the compaction budget uses, so the numbers stay consistent), then let the
	// real `usage` event overwrite it when it arrives.
	let streamed = "";
	// The reasoning block currently streaming, if any. Held so deltas append to
	// one block rather than pushing a transcript line every few tokens.
	let thinkingView: ThinkingView | undefined;

	session.harness.events.on("message_update", (event) => {
		const delta = (event as { event?: { type?: string; delta?: string; text?: string; thinking?: string } }).event;

		// Reasoning streams on its own channel. qwen3.8 emits a block before most
		// answers; dropped, the user sees a long pause and then a result.
		if (delta?.type === "thinking_start") {
			thinkingView = { text: "", active: true };
			transcript.appendBlank();
			transcript.appendThinking(thinkingView);
			spinner.setLabel("Thinking");
			footer.update({ thinking: true });
			tui.requestRender();
			return;
		}
		if (delta?.type === "thinking_delta") {
			if (thinkingView) {
				// `delta` is the field the stream actually uses; `thinking` is the
				// accumulated form that appears on the block, not on the event.
				// Only deltas are accumulated - `thinking_start` carries a partial
				// snapshot that would double the first fragment if added too.
				thinkingView.text += delta.delta ?? delta.thinking ?? "";
				tui.requestRender();
			}
			return;
		}
		if (delta?.type === "thinking_end") {
			if (thinkingView) thinkingView.active = false;
			thinkingView = undefined;
			spinner.resetLabel();
			footer.update({ thinking: false });
			tui.requestRender();
			return;
		}

		const text = delta?.delta ?? delta?.text;
		if (typeof text !== "string" || !text) return;
		streamed += text;
		// estimateTokens takes a message, not a string: wrap the accumulated
		// text so the estimate uses pi's own accounting rather than a second,
		// divergent heuristic of ours.
		spinner.setTokens(
			estimateTokens({ role: "assistant", content: [{ type: "text", text: streamed }] } as unknown as AgentMessage),
		);
		tui.requestRender();
	});

	session.harness.events.on("message_end", (event) => {
		// Assistant prose carries no marker - only tool calls get one. Text
		// arrives as content blocks; anything non-text is rendered by its own
		// handler, so only text is pulled out here.
		const message = (event as { message?: { role?: string; content?: unknown[] } }).message;
		if (message?.role !== "assistant") return;
		// Thinking blocks are NOT read here: they already streamed in through
		// `thinking_delta` above, and taking them again would render each one
		// twice. Only prose is left to add.
		const text = (message.content ?? [])
			.filter((b): b is { type: "text"; text: string } => (b as { type?: string }).type === "text")
			.map((b) => b.text)
			.join("")
			.trim();
		if (!text) return;
		transcript.appendBlank();
		transcript.appendMarkdown(text);
		tui.requestRender();
	});

	session.harness.events.on("tool_start", (event) => {
		turnToolCalls++;
		const e = event as { toolCallId: string; toolName: string; args: unknown };
		pending.set(e.toolCallId, {
			name: titleCase(e.toolName),
			primaryArg: primaryArg(e.args),
			status: "running",
		});
	});

	session.harness.events.on("tool_end", (event) => {
		const e = event as { toolCallId: string; result: unknown; isError: boolean };
		const view = pending.get(e.toolCallId);
		if (!view) return;
		pending.delete(e.toolCallId);

		view.status = e.isError ? "error" : "ok";
		const summary = summarize(e.result);
		// Truncate to the tier's budget: on a 32k model a dumped file is the
		// difference between a working session and a blown window. The full text
		// is kept for Ctrl+R — truncation is a display choice, not a data loss.
		const max = Math.max(3, Math.floor(session.tier.toolOutputTokens / 400));
		view.resultLines = summary.slice(0, max);
		view.totalLines = summary.length;

		transcript.appendBlank();
		transcript.appendToolCall(view, summary);
		tui.requestRender();
	});

	session.harness.events.on("usage", (event) => {
		// The payload is { row, totals }, not { usage } - reading `.usage` gave
		// undefined, which is why the spinner showed no token count.
		const e = event as {
			totals?: { input?: number; output?: number; cost?: { total?: number } };
			row?: { usage?: { input?: number; output?: number } };
		};
		if (e.totals) spinner.setTokens((e.totals.input ?? 0) + (e.totals.output ?? 0));

		// Context used is the LAST request's input, not the cumulative total:
		// `totals` grows forever across a session, so using it would show the
		// window filling up when compaction had just emptied it.
		const row = e.row?.usage;
		if (row) footer.update({ contextUsed: (row.input ?? 0) + (row.output ?? 0) });
		if (e.totals?.cost?.total !== undefined) footer.update({ cost: e.totals.cost.total });

		tui.requestRender();
	});

	session.harness.events.on("fault", (event) => {
		transcript.append(renderError(String((event as { error?: unknown }).error ?? "unknown fault")));
		tui.requestRender();
	});

	let spinnerTimer: NodeJS.Timeout | undefined;
	let turn = 0;

	let turnStartedAt = 0;
	let turnToolCalls = 0;

	const beginTurn = () => {
		streamed = "";
		turnStartedAt = Date.now();
		turnToolCalls = 0;
		spinner.start(turn++);
		spinnerTimer = setInterval(() => {
			spinner.tick();
			tui.requestRender();
		}, SPINNER_INTERVAL_MS);
	};

	const endTurn = () => {
		if (spinnerTimer) clearInterval(spinnerTimer);
		spinnerTimer = undefined;
		spinner.stop();
		pending.clear();
		tui.requestRender();
	};

	// --- input -----------------------------------------------------------

	editor.onSubmit = (text: string) => {
		const line = text.trim();
		if (!line) return;
		editor.setText("");

		// Echo the user's line, as Claude Code does: without it the transcript
		// reads as a monologue once you scroll back.
		transcript.appendBlank();
		transcript.appendUser(line);
		transcript.appendBlank();
		tui.requestRender();

		void (async () => {
			try {
				// `!` and `#` act directly and never reach the model. On a local
				// model that is a second instead of a minute.
				const mode = classifyInput(line);
				if (mode) {
					const result =
						mode.mode === "bang"
							? opts.env
								? await runBang(mode.body, opts.env)
								: { output: ["no shell available"] }
							: await addMemory(mode.body, opts.cwd);
					transcript.append(result.output);
					tui.requestRender();
					return;
				}

				const handled = await commands.execute(line);
				if (handled) {
					if (handled.output) transcript.append(handled.output.split("\n"));
					// A command may expand into a prompt (`.claude/commands` do).
					if (!handled.prompt) {
						tui.requestRender();
						return;
					}
				}

				// UserPromptSubmit hooks see the line as typed. They may refuse the
				// turn outright, or return text that becomes context for it - the
				// relay inbox hook delivers unread messages this way.
				let hookContext: string[] = [];
				if (opts.runPromptHooks) {
					const result = await opts.runPromptHooks(line);
					if (result.blocked) {
						transcript.append(renderError(`blocked by hook: ${result.blocked.reason}`));
						tui.requestRender();
						return;
					}
					hookContext = result.context;
				}

				// SessionStart context is spent on the first turn and not repeated,
				// so a startup inbox summary does not ride along on every message.
				if (startupContext.length > 0) {
					hookContext = [...startupContext, ...hookContext];
					startupContext = [];
				}

				// `@path` inlines the file so the model has it without spending a
				// turn on a read. Only on a plain line: a slash command that
				// expanded into a prompt already built exactly what it meant to
				// send, and re-scanning it would attach files it never asked for.
				let prompt = handled?.prompt ?? line;
				// Images travel as their own argument to `prompt()`, not inside the
				// text: a base64 PNG in the prompt string is not something any
				// provider will interpret as an image.
				let images: ImageContent[] = [];
				if (!handled?.prompt) {
					const resolved = await resolveMentions(line, {
						cwd: opts.cwd,
						tier: session.tier,
						roots: opts.gate?.getRoots(),
					});
					if (resolved.mentions.length > 0) {
						prompt = resolved.prompt;
						images = resolved.images.map((i) => ({
							type: "image" as const,
							data: i.data,
							mimeType: i.mimeType,
						}));
						// What was attached and what it cost, but never the content:
						// the user has the file, and echoing it buries the conversation.
						transcript.append(describeMentions(resolved.mentions, opts.cwd));
						transcript.appendBlank();
						tui.requestRender();
					}
				}

				// Hook context goes ahead of the prompt, tagged so the model can
				// tell it apart from something the user actually typed.
				if (hookContext.length > 0) {
					prompt = `<hook-context>\n${hookContext.join("\n\n")}\n</hook-context>\n\n${prompt}`;
				}

				beginTurn();
				const result = await session.lane.prompt(prompt, images.length > 0 ? images : undefined, BACKGROUND_CONTEXT);
				endTurn();

				if (!result.ok) {
					transcript.append(renderError(JSON.stringify((result as { error?: unknown }).error)));
				}
				// Closes the turn with what it cost. On a local model at ~20 tok/s
				// the elapsed time is the number actually being budgeted against.
				transcript.append(
					renderTurnSummary({
						seconds: Math.max(1, Math.round((Date.now() - turnStartedAt) / 1000)),
						tokens: spinner.getTokens(),
						toolCalls: turnToolCalls,
					}),
				);
				transcript.appendBlank();
				refreshStatus();
				// A turn is what usually dirties the tree, so this is the moment
				// the branch state is worth re-reading.
				void readGitStatus(opts.cwd).then((git) => {
					if (git) {
						footer.update({ git });
						tui.requestRender();
					}
				});
			} catch (err) {
				endTurn();
				transcript.append(renderError((err as Error).message));
			}
			tui.requestRender();
		})();
	};

	// Esc interrupts a running turn; when idle it is the editor's to handle.
	// Permission mode cycling, matching Claude Code's Shift+Tab.
	const MODES: PermissionMode[] = ["manual", "acceptEdits", "auto", "plan"];

	// Wrapped rather than registered directly, and a render is requested HERE.
	// pi-tui stops dispatching the moment a listener returns `{consume: true}`,
	// so a second listener that re-renders never runs for exactly the keys that
	// changed something. Every consumed key mutated state and then left the
	// screen untouched, which is indistinguishable from the key being dead.
	const routeKey = createKeyRouter({
		permissionKey: (data) => permission.isActive() && permission.handleKey(data),
		isBusy: () => spinner.isBusy(),
		hasInput: () => editor.getText().trim().length > 0,

		interrupt: () => void session.lane.abort(BACKGROUND_CONTEXT),

		toggleExpanded: () => {
			// Say so when there is nothing to expand, rather than flipping a
			// flag that changes nothing on screen and reads as a broken key.
			if (!transcript.hasCollapsed() && !transcript.isExpanded()) {
				refreshStatus("nothing truncated");
			} else {
				const now = transcript.toggleExpanded();
				refreshStatus(now ? "expanded" : "");
			}
		},

		cyclePermissionMode: () => {
			if (!opts.gate) return;
			const next = MODES[(MODES.indexOf(opts.gate.mode) + 1) % MODES.length];
			opts.gate.setMode(next);
			refreshStatus();
		},

		clearInput: () => editor.setText(""),

		clearScreen: () => {
			// Clears the rendered transcript, not the conversation: /clear is
			// the command that forgets things, and conflating the two would
			// make a display shortcut destroy context.
			transcript.clear();
			refreshStatus();
		},

		rewind: () => {
			transcript.append([dim("  rewind: use /rewind <entry-id>; /resume lists past sessions")]);
		},

		exit: () => {
			tui.stop();
			process.kill(process.pid, "SIGINT");
		},

		hint: (message) => refreshStatus(message),
	});

	tui.addInputListener((data) => {
		const result = routeKey(data);
		if (result?.consume) tui.requestRender();
		return result;
	});

	transcript.append([
		`${green(g().call)} ${bold("harness")} ${dim(`— ${opts.modelLabel}`)}`,
		dim("  Type / for commands, @ to reference a file, or just ask."),
	]);

	tui.start();

	await new Promise<void>((resolve) => {
		process.on("SIGINT", () => {
			tui.stop();
			resolve();
		});
	});
}
