import hljs from "highlight.js";
import { blue, cyan, dim, green, isColorEnabled, magenta, red, yellow } from "./theme.ts";

/**
 * Terminal syntax highlighting for markdown code blocks.
 *
 * `highlight.js` was chosen over hand-rolling because it has **zero
 * dependencies** — the usual objection to it (a large transitive tree) does not
 * apply, and it brings 193 languages instead of the handful a hand-written
 * tokenizer would cover before becoming a maintenance burden of its own.
 *
 * It emits HTML, so this maps its token classes onto ANSI. Parsing HTML with a
 * regex is normally a mistake; it is safe here only because the input is
 * hljs's own escaped, well-formed output rather than arbitrary markup.
 */

/**
 * Token class -> colour.
 *
 * Grouped by role rather than by language so the palette stays consistent:
 * a keyword should look the same in Go and TypeScript, or the colours stop
 * carrying meaning.
 */
const STYLES: Record<string, (s: string) => string> = {
	keyword: magenta,
	built_in: cyan,
	type: cyan,
	literal: magenta,
	number: yellow,
	string: green,
	"meta string": green,
	regexp: red,
	comment: dim,
	doctag: dim,
	title: blue,
	"title function_": blue,
	"title class_": cyan,
	function: blue,
	class: cyan,
	params: (s) => s,
	property: (s) => s,
	attr: cyan,
	attribute: cyan,
	variable: (s) => s,
	operator: (s) => s,
	punctuation: (s) => s,
	symbol: yellow,
	meta: dim,
	section: blue,
	name: blue,
	tag: blue,
	selector_tag: blue,
	"selector-tag": blue,
	deletion: red,
	addition: green,
};

const ENTITIES: Record<string, string> = {
	"&amp;": "&",
	"&lt;": "<",
	"&gt;": ">",
	"&quot;": '"',
	"&#x27;": "'",
	"&#39;": "'",
};

function decode(text: string): string {
	return text.replace(/&(?:amp|lt|gt|quot|#x27|#39);/g, (m) => ENTITIES[m] ?? m);
}

/**
 * Convert hljs HTML to ANSI.
 *
 * Nesting is tracked with a stack because hljs emits nested spans (a `title`
 * inside a `function`, for instance). The innermost class wins, which matches
 * how CSS would resolve it.
 */
function htmlToAnsi(html: string): string {
	const stack: string[] = [];
	let out = "";
	let index = 0;

	const pattern = /<span class="hljs-([^"]+)">|<\/span>/g;
	let match: RegExpExecArray | null;

	while ((match = pattern.exec(html)) !== null) {
		const text = html.slice(index, match.index);
		if (text) out += paint(decode(text), stack);
		index = match.index + match[0].length;

		if (match[1]) stack.push(match[1].replace(/-/g, "_"));
		else stack.pop();
	}

	const tail = html.slice(index);
	if (tail) out += paint(decode(tail), stack);
	return out;
}

function paint(text: string, stack: string[]): string {
	// Innermost class first; fall back outward when a class has no style.
	for (let i = stack.length - 1; i >= 0; i--) {
		const style = STYLES[stack[i]];
		if (style) return style(text);
	}
	return text;
}

/**
 * Highlight a code block, returning one string per line.
 *
 * Shaped for pi-tui's `MarkdownTheme.highlightCode`, which expects lines back.
 * Any failure returns the source unchanged: unhighlighted code is a cosmetic
 * loss, while a thrown error inside a render pass takes down the frame.
 */
export function highlightCode(code: string, lang?: string): string[] {
	if (!isColorEnabled()) return code.split("\n");

	try {
		// Only trust an explicit, known language. `highlightAuto` guesses badly on
		// the short snippets that dominate a transcript, and a wrong guess colours
		// the code misleadingly rather than leaving it plain.
		if (!lang || !hljs.getLanguage(lang)) return code.split("\n");

		const html = hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
		return htmlToAnsi(html).split("\n");
	} catch {
		return code.split("\n");
	}
}
