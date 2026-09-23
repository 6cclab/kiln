import { readFile, stat } from "node:fs/promises";
import { extname, isAbsolute, relative, resolve } from "node:path";
import { estimateTokens } from "@earendil-works/pi-agent-core";
import type { AgentMessage } from "@earendil-works/pi-agent-core";
import type { Tier } from "../budget/tier.ts";

/**
 * `@path` mentions (parity spec §2).
 *
 * pi-tui's autocomplete already completes the path into the line. What was
 * missing is what happens on submit: Claude Code *inlines the file* so the
 * model has it without spending a turn on a read.
 *
 * Without this, `@src/foo.ts explain this` reaches the model as literal text.
 * A capable model recovers by calling `read`; a small local one often just
 * answers about a filename it cannot see. Either way it costs a round-trip,
 * which on a local model at ~21 tok/s is the difference between a second and a
 * minute.
 *
 * ## Why this is budgeted rather than verbatim
 *
 * Inlining is unconditional spend: the tokens are paid whether or not the model
 * needed the file, and they are paid before it can decide. On a 200k window
 * that is nothing. On a 32k window one `@` on a large file ends the
 * conversation before it starts.
 *
 * So a mention is capped at the tier's `toolOutputTokens` — the same ceiling a
 * tool result gets, because it is the same kind of spend. Over the cap, the
 * head of the file is inlined and the model is told plainly that it was cut and
 * that `read` will get the rest. That keeps the cheap case cheap without
 * turning the expensive case into a silent truncation the model mistakes for a
 * whole file.
 */

/**
 * `estimateTokens` counts a message, not a string, so plain text has to be
 * wrapped. Same approach as the token counter in the TUI - one estimator for
 * the whole harness beats two that disagree.
 */
function countTokens(text: string): number {
	return estimateTokens({ role: "user", content: [{ type: "text", text }] } as unknown as AgentMessage);
}

/**
 * Image extensions a vision model can take.
 *
 * Read as base64 and attached as an image block rather than inlined as text —
 * a PNG read as UTF-8 is megabytes of replacement characters, which on a 32k
 * window ends the conversation and tells the model nothing.
 */
const IMAGE_TYPES: Record<string, string> = {
	".png": "image/png",
	".jpg": "image/jpeg",
	".jpeg": "image/jpeg",
	".gif": "image/gif",
	".webp": "image/webp",
};

export interface ImageAttachment {
	/** Base64, as the provider expects. */
	data: string;
	mimeType: string;
	path: string;
	bytes: number;
}

export interface Mention {
	/** The text as typed, without the `@`. */
	raw: string;
	/** Resolved absolute path, when it resolved to a readable file. */
	path?: string;
	/** Why it was not inlined, when it was not. */
	skipped?: string;
	/** Inlined content, already capped. */
	content?: string;
	truncated?: boolean;
	tokens?: number;
	/** Set when the mention resolved to an image rather than text. */
	image?: ImageAttachment;
}

export interface MentionResult {
	/** The prompt to send: the original line plus any attached files. */
	prompt: string;
	mentions: Mention[];
	/** Images to send alongside the prompt, in mention order. */
	images: ImageAttachment[];
}

/**
 * Matches `@` followed by a path.
 *
 * Anchored to a word boundary so an email address or a decorator is not
 * mistaken for a mention. Trailing punctuation is excluded from the path
 * because `@src/foo.ts.` and `@src/foo.ts,` are how people actually write
 * inside a sentence.
 */
const MENTION = /(?:^|\s)@([^\s@]+?)(?=[.,;:!?)]*(?:\s|$))/g;

export function parseMentions(line: string): string[] {
	const out: string[] = [];
	for (const match of line.matchAll(MENTION)) {
		if (!out.includes(match[1])) out.push(match[1]);
	}
	return out;
}

/** Cap content to a token budget, cutting on a line boundary. */
function capToTokens(text: string, budget: number): { text: string; truncated: boolean } {
	if (countTokens(text) <= budget) return { text, truncated: false };
	const lines = text.split("\n");
	const kept: string[] = [];
	let used = 0;
	for (const line of lines) {
		// +1 for the newline. Counting per line rather than binary-searching the
		// whole string keeps the cut on a boundary a reader can make sense of.
		const cost = countTokens(line) + 1;
		if (used + cost > budget) break;
		kept.push(line);
		used += cost;
	}
	// A budget too small for even the first line still has to yield something,
	// or the mention silently vanishes.
	if (kept.length === 0) kept.push(lines[0]?.slice(0, 200) ?? "");
	return { text: kept.join("\n"), truncated: true };
}

export interface ResolveOptions {
	cwd: string;
	tier: Tier;
	/** Workspace roots. A mention outside them is not inlined. */
	roots?: readonly string[];
}

/**
 * Read the mentioned files and build the prompt to send.
 *
 * Failures are reported, never thrown: a typo'd path should degrade to "the
 * model did not get that file" with a visible note, not take down the turn.
 */
export async function resolveMentions(line: string, opts: ResolveOptions): Promise<MentionResult> {
	const raws = parseMentions(line);
	if (raws.length === 0) return { prompt: line, mentions: [], images: [] };

	// Split the budget across mentions so `@a @b @c` cannot cost three times
	// what one mention is allowed to.
	const perMention = Math.max(256, Math.floor(opts.tier.toolOutputTokens / raws.length));

	const mentions: Mention[] = [];
	for (const raw of raws) {
		const path = isAbsolute(raw) ? raw : resolve(opts.cwd, raw);
		const mention: Mention = { raw, path };
		mentions.push(mention);

		// The same rule the permission gate applies: a path outside the
		// workspace is not read just because the user typed it quickly.
		// `/add-dir` is the way to widen it.
		if (opts.roots?.length && !opts.roots.some((root) => path === root || path.startsWith(`${root}/`))) {
			mention.skipped = "outside the workspace";
			continue;
		}

		try {
			const info = await stat(path);
			if (info.isDirectory()) {
				// A directory listing is a different thing from a file's
				// contents, and inlining one is rarely what was meant.
				mention.skipped = "is a directory";
				continue;
			}
			// An image is attached, not inlined. Checked before reading, because
			// reading a PNG as UTF-8 produces megabytes of replacement characters.
			const mimeType = IMAGE_TYPES[extname(path).toLowerCase()];
			if (mimeType) {
				const bytes = await readFile(path);
				mention.image = { data: bytes.toString("base64"), mimeType, path, bytes: bytes.byteLength };
				continue;
			}

			const text = await readFile(path, "utf8");
			const capped = capToTokens(text, perMention);
			mention.content = capped.text;
			mention.truncated = capped.truncated;
			mention.tokens = countTokens(capped.text);
		} catch (err) {
			mention.skipped = (err as NodeJS.ErrnoException).code === "ENOENT" ? "not found" : (err as Error).message;
		}
	}

	const images = mentions.map((m) => m.image).filter((i): i is ImageAttachment => i !== undefined);

	const attached = mentions.filter((m) => m.content !== undefined);
	if (attached.length === 0) return { prompt: line, mentions, images };

	const blocks = attached.map((m) => {
		const shown = relative(opts.cwd, m.path ?? m.raw) || m.raw;
		const note = m.truncated
			? `\n[truncated to fit the context budget - use the read tool for the rest of ${shown}]`
			: "";
		return `<file path="${shown}">\n${m.content}${note}\n</file>`;
	});

	// Files first, question last: the instruction is what the model should be
	// holding when it starts generating, and trailing content is weighted more
	// heavily by every model this has to run on.
	return { prompt: `${blocks.join("\n\n")}\n\n${line}`, mentions, images };
}

/** One transcript line per mention. The content is not echoed - the user has the file. */
export function describeMentions(mentions: Mention[], cwd: string): string[] {
	return mentions.map((m) => {
		const shown = m.path ? relative(cwd, m.path) || m.raw : m.raw;
		if (m.skipped) return `  @${shown} - ${m.skipped}`;
		if (m.image) return `  @${shown} (image, ${Math.round(m.image.bytes / 1024)} KB)`;
		const size = m.tokens === undefined ? "" : ` (~${m.tokens} tokens${m.truncated ? ", truncated" : ""})`;
		return `  @${shown}${size}`;
	});
}
