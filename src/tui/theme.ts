/**
 * Terminal styling.
 *
 * Hand-rolled ANSI rather than chalk: pi-tui already measures visible width
 * correctly around escape sequences (`visibleWidth`, `truncateToWidth`), so the
 * only thing a color library would add here is a dependency.
 *
 * Every style routes through `style()`, which is a no-op when color is
 * disabled. That is what makes `--ax-screen-reader` and `NO_COLOR` a single
 * switch instead of a second rendering path.
 */

const ESC = "[";

/** Honor NO_COLOR (informal standard) and non-TTY output. */
function colorEnabled(): boolean {
	if (process.env.NO_COLOR) return false;
	if (process.env.FORCE_COLOR) return true;
	return Boolean(process.stdout.isTTY);
}

let enabled = colorEnabled();

/** Disable styling globally: screen-reader mode, piped output, tests. */
export function setColorEnabled(value: boolean): void {
	enabled = value;
}

export function isColorEnabled(): boolean {
	return enabled;
}

function style(open: string, close: string) {
	return (text: string): string => (enabled ? `${ESC}${open}m${text}${ESC}${close}m` : text);
}

export const dim = style("2", "22");
export const bold = style("1", "22");
export const italic = style("3", "23");
export const strike = style("9", "29");

export const red = style("31", "39");
export const green = style("32", "39");
export const yellow = style("33", "39");
export const blue = style("34", "39");
export const magenta = style("35", "39");
export const cyan = style("36", "39");
export const gray = style("90", "39");

/**
 * Subtle background, for the user's own messages.
 *
 * A background rather than a colour because the point is to make the message
 * findable when scrolling back through a long transcript: colour competes with
 * the tool markers and diff output that already use it, while a filled row
 * reads as structure at a glance and disappears when you are not looking for it.
 */
export const userBlock = style("48;5;236", "49");

/**
 * Glyphs.
 *
 * Highest-risk detail in the whole UI: a wrong glyph is the most immediately
 * visible parity failure, and these are wide/uncommon codepoints that not every
 * font renders. `asciiGlyphs()` is the fallback, also used by screen-reader mode.
 */
export interface Glyphs {
	/** Tool call marker, flush left. */
	call: string;
	/** Result continuation, indented under the call. */
	result: string;
	/** Marks a reasoning block. */
	thinking: string;
	/** Prefix on the user's own message block. */
	userMark: string;
	/** Marks the line that closes a turn. */
	summary: string;
	todoDone: string;
	todoPending: string;
	todoActive: string;
	spinner: string[];
}

export const UNICODE_GLYPHS: Glyphs = {
	call: "⏺",
	result: "⎿",
	thinking: "✻",
	userMark: "›",
	summary: "✳",
	todoDone: "☒",
	todoPending: "☐",
	todoActive: "◐",
	// Braille cycle: smooth at 80ms and renders in nearly every modern font.
	spinner: ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"],
};

export const ASCII_GLYPHS: Glyphs = {
	call: "*",
	result: "\\",
	thinking: "*",
	userMark: ">",
	summary: "*",
	todoDone: "[x]",
	todoPending: "[ ]",
	todoActive: "[~]",
	spinner: ["-", "\\", "|", "/"],
};

let glyphs: Glyphs = UNICODE_GLYPHS;

export function setGlyphs(next: Glyphs): void {
	glyphs = next;
}

export function g(): Glyphs {
	return glyphs;
}

/**
 * Screen-reader / plain mode.
 *
 * Mirrors Claude Code's `--ax-screen-reader`: flat text, no decorative borders
 * or animation. Doubles as the degraded mode for a bad SSH link or a dumb
 * terminal, which is why it is one switch rather than an accessibility
 * afterthought bolted on later.
 */
let plain = false;

export function setPlainMode(on: boolean): void {
	plain = on;
	setColorEnabled(on ? false : colorEnabled());
	setGlyphs(on ? ASCII_GLYPHS : UNICODE_GLYPHS);
}

/** Whether decorative glyphs should be avoided. */
export function isPlain(): boolean {
	return plain;
}
