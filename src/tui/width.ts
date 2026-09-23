import { truncateToWidth, visibleWidth, wrapTextWithAnsi } from "@earendil-works/pi-tui";

/**
 * Keeping rendered lines inside the viewport.
 *
 * pi-tui throws when a component returns a line wider than the terminal:
 *
 *     Error: Rendered line 3 exceeds terminal width (172 > 144).
 *
 * It is right to refuse — an over-wide line corrupts its differential
 * rendering, so the alternative to an error is a garbled screen. But it means
 * every component is responsible for its own width, and most of what this
 * harness renders is content it did not author: a pasted URL, a path in a tool
 * call, a diff hunk, a model-written plan, a line the user typed.
 *
 * So the width check belongs in one place rather than at each of the dozen
 * sites that build a line.
 *
 * Two behaviors, and the choice between them is about what the line is for:
 *
 *   - `fitLines` **wraps**. Use it for content — losing the end of a URL or a
 *     diff line is worse than taking an extra row.
 *   - `fitStatus` **truncates**. Use it for single-row indicators like the
 *     spinner and footer, where wrapping would shove the layout around to say
 *     something the user can already infer.
 */

/** Wrap any line that would overflow, preserving every character. */
export function fitLines(lines: string[], width: number, indent = ""): string[] {
	if (width <= 0) return [];
	const out: string[] = [];
	for (const line of lines) {
		if (visibleWidth(line) <= width) {
			out.push(line);
			continue;
		}
		// A continuation indent keeps wrapped output legible inside an already
		// indented block (a prompt, a plan), but it has to leave room for itself
		// or the wrap just overflows again one level down.
		const room = Math.max(1, width - visibleWidth(indent));
		const wrapped = wrapTextWithAnsi(line, room);
		out.push(wrapped[0]);
		for (const rest of wrapped.slice(1)) out.push(`${indent}${rest}`);
	}
	return out;
}

/** Truncate to a single row, with an ellipsis when something was cut. */
export function fitStatus(line: string, width: number): string {
	if (width <= 0) return "";
	return visibleWidth(line) <= width ? line : truncateToWidth(line, width);
}
