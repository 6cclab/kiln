import { readFileSync } from "node:fs";
import { dim, green, red } from "./theme.ts";

/**
 * Render what a tool call will actually change, for the permission prompt.
 *
 * Approving `Edit(config.ts)` without seeing the change is approving blind —
 * the file path says nothing about whether the edit is a typo fix or deleting
 * the file's contents. The prompt must show the change, not just name it.
 *
 * Diffs are computed from the *pending* arguments, before the tool runs, so
 * this cannot reuse the edit tool's own output (which only exists afterwards).
 */

/** Keep a preview short enough to read; a 400-line diff is not a prompt. */
const MAX_PREVIEW_LINES = 16;

interface EditOp {
	oldText: string;
	newText: string;
}

function clip(lines: string[]): string[] {
	if (lines.length <= MAX_PREVIEW_LINES) return lines;
	const hidden = lines.length - MAX_PREVIEW_LINES;
	return [...lines.slice(0, MAX_PREVIEW_LINES), dim(`  … ${hidden} more line(s)`)];
}

/**
 * Render one replacement as removed lines followed by added lines.
 *
 * Deliberately not a line-by-line diff: `oldText` and `newText` are exact
 * strings the tool will match literally, so "these lines go, those arrive" is
 * the honest rendering. Aligning them into a unified diff would imply a
 * correspondence between old and new lines that the edit does not actually have.
 *
 * No surrounding context is shown, because the arguments do not contain any —
 * the file is not read here, and reading it would preview a state that may
 * differ from what the tool ultimately matches against.
 */
function renderReplacement(op: EditOp): string[] {
	const lines: string[] = [];
	for (const line of op.oldText.split("\n").slice(0, MAX_PREVIEW_LINES)) lines.push(red(`  - ${line}`));
	for (const line of op.newText.split("\n").slice(0, MAX_PREVIEW_LINES)) lines.push(green(`  + ${line}`));
	return lines;
}

/**
 * A human-readable preview of a pending change, or undefined when the call has
 * nothing to show beyond its arguments (bash, reads, MCP calls).
 */
export function renderChangePreview(toolName: string, args: Record<string, unknown>): string[] | undefined {
	const tool = toolName.toLowerCase();

	if (tool === "edit") {
		const edits = args.edits as EditOp[] | undefined;
		if (!Array.isArray(edits) || edits.length === 0) return undefined;
		const lines = edits.flatMap((op, i) => [
			...(edits.length > 1 ? [dim(`  edit ${i + 1} of ${edits.length}`)] : []),
			...renderReplacement(op),
		]);
		return clip(lines);
	}

	if (tool === "write") {
		const path = typeof args.path === "string" ? args.path : undefined;
		const content = typeof args.content === "string" ? args.content : "";

		// Whether this creates or overwrites is the single most important fact
		// about a write, and it is invisible in the arguments alone.
		let existing: string | undefined;
		try {
			existing = path ? readFileSync(path, "utf8") : undefined;
		} catch {
			existing = undefined;
		}

		if (existing === undefined) {
			return clip([dim(`  new file, ${content.split("\n").length} line(s)`), ...content.split("\n").map((l) => green(`  + ${l}`))]);
		}
		if (existing === content) return [dim("  no change")];

		return clip([
			dim(`  overwrites ${existing.split("\n").length} existing line(s)`),
			...existing.split("\n").slice(0, 6).map((l) => red(`  - ${l}`)),
			...content.split("\n").slice(0, 6).map((l) => green(`  + ${l}`)),
		]);
	}

	return undefined;
}
