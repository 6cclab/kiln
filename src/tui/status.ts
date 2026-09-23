import { dim, gray, green, yellow, red, bold, cyan, isPlain } from "./theme.ts";

/**
 * The status line.
 *
 * Claude Code's is a dashboard, not a label: context used against the window
 * with a meter, git branch and whether the tree is dirty, spend so far, session
 * age, and the permission mode on its own line with the key that changes it.
 * Reading it answers "can I keep going, and what will it do if I do" without
 * running a command.
 *
 * What this had before was a comma-separated list of facts that never changed
 * during a session — cwd, model, tier, budget. True, and useless: nothing on it
 * moved, so there was no reason to look at it twice.
 *
 * Every segment here either changes as you work or tells you something you
 * cannot otherwise see. A segment with nothing to say is omitted rather than
 * shown empty, because a dashboard of blanks is worse than a short one.
 */

export interface StatusState {
	modelLabel: string;
	contextWindow: number;
	/** Tokens in the current conversation, from the last request's input. */
	contextUsed?: number;
	/** Cumulative spend, in dollars. Omitted when the model is free. */
	cost?: number;
	git?: { branch: string; dirty: boolean };
	/** Permission mode, shown on the second line with the key that cycles it. */
	mode: string;
	/** Set while the model is reasoning. */
	thinking?: boolean;
	/** Session start, for the elapsed clock. */
	startedAt: number;
	/** Injected for tests. */
	now?: number;
}

const FILLED = "█";
const EMPTY = "░";
const METER_WIDTH = 6;

/** A small bar. Fractional fills round down, so a full bar means genuinely full. */
export function meter(fraction: number, width = METER_WIDTH): string {
	const clamped = Math.max(0, Math.min(1, fraction));
	const filled = Math.floor(clamped * width);
	return FILLED.repeat(filled) + EMPTY.repeat(width - filled);
}

/** `450k`, `1.0m`, `21.1k` — the compact forms the meter sits next to. */
export function compact(n: number): string {
	if (n >= 1_000_000) {
		const m = n / 1_000_000;
		return `${m >= 10 ? Math.round(m) : m.toFixed(1)}m`;
	}
	if (n >= 1_000) {
		const k = n / 1_000;
		return `${k >= 100 ? Math.round(k) : k.toFixed(k >= 10 ? 0 : 1)}k`;
	}
	return String(n);
}

export function elapsed(ms: number): string {
	const seconds = Math.floor(ms / 1000);
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m`;
	const hours = Math.floor(minutes / 60);
	return hours < 24 ? `${hours}h${minutes % 60 > 0 ? ` ${minutes % 60}m` : ""}` : `${Math.floor(hours / 24)}d`;
}

/**
 * Colour by how much room is left, not by an absolute number.
 *
 * 80% of a 1M window and 80% of a 32k window are the same problem to the person
 * reading it, and the point of the colour is to say "start thinking about
 * compaction" at the moment that becomes true.
 */
function pressure(fraction: number): (s: string) => string {
	if (fraction >= 0.9) return red;
	if (fraction >= 0.7) return yellow;
	return green;
}

function clock(at: number): string {
	const d = new Date(at);
	const h = d.getHours();
	const m = String(d.getMinutes()).padStart(2, "0");
	const suffix = h < 12 ? "am" : "pm";
	const hour12 = h % 12 === 0 ? 12 : h % 12;
	return `${hour12}:${m}${suffix}`;
}

/**
 * The status line, as one or two rows.
 *
 * The mode goes on its own row because it is the one thing on here that changes
 * what the agent will *do* rather than reporting what it has done, and it is
 * the one thing with a key that changes it — so it gets the hint next to it
 * rather than being a word at the end of a list.
 */
export function renderStatus(state: StatusState): string[] {
	const now = state.now ?? Date.now();
	const plain = isPlain();
	const sep = dim(" │ ");
	const segments: string[] = [];

	segments.push(`${bold(state.modelLabel)} ${dim(`(${compact(state.contextWindow)} ctx)`)}`);

	if (state.git) {
		// A dirty tree is the thing you forget and then discover during a
		// rebase, so it gets a glyph rather than being implied by absence.
		const mark = state.git.dirty ? yellow("●") : green("✓");
		segments.push(`${dim(plain ? "git" : "⎇")} ${cyan(state.git.branch)} ${mark}`);
	}

	if (state.contextUsed !== undefined && state.contextWindow > 0) {
		const fraction = state.contextUsed / state.contextWindow;
		const colour = pressure(fraction);
		const percent = Math.round(fraction * 100);
		segments.push(
			`${colour(meter(fraction))} ${compact(state.contextUsed)}${dim("/")}${compact(state.contextWindow)} ${dim(`${percent}%`)}`,
		);
	}

	if (state.thinking) segments.push(dim(plain ? "thinking" : "◇ thinking"));

	// Omitted rather than shown as $0.00: a self-hosted model has no marginal
	// cost, and a permanent zero is a segment that never earns its width.
	if (state.cost !== undefined && state.cost > 0) segments.push(`$${state.cost.toFixed(2)}`);

	segments.push(dim(`${elapsed(now - state.startedAt)} ${plain ? "" : "· "}${clock(now)}`));

	const modeColour = state.mode === "bypassPermissions" ? red : state.mode === "plan" ? cyan : green;
	const modeLine = `${dim(plain ? ">>" : "▶▶")} ${modeColour(`${state.mode} mode`)} ${dim("(shift+tab to cycle)")}`;

	return [segments.join(sep), modeLine];
}
