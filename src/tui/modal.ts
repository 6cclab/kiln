import { SelectList, type Component, type SelectItem, type TUI } from "@earendil-works/pi-tui";
import { fitLines, fitStatus } from "./width.ts";
import { bold, cyan, dim, gray, green, red, yellow } from "./theme.ts";

/**
 * Full-screen-ish panels for the commands that manage things rather than
 * report them.
 *
 * `/permissions`, `/mcp`, `/agents` and `/config` all answer "what is
 * configured, and change it" — and printing a listing into the transcript
 * answers only the first half. You then leave the session and hand-edit
 * `settings.json`, which is the workflow these exist to remove.
 *
 * pi-tui supplies the parts: `showOverlay` for a focused layer above the
 * transcript, `SelectList` for a navigable list. What is here is the frame and
 * the contract — a title, a footer of what the keys do, a row of actions, and
 * a way for a command to ask for one without knowing any of that.
 */

export interface ModalAction {
	/** The key that runs it, e.g. "d". Matched case-insensitively. */
	key: string;
	label: string;
	/** Returns a status line to show, or undefined for silence. */
	run: (selected: SelectItem | undefined) => Promise<string | undefined> | string | undefined;
	/** Close the panel afterwards. */
	closes?: boolean;
}

export interface ModalSpec {
	title: string;
	/** Rows to navigate. Rebuilt by `refresh` after an action changes something. */
	items: () => Promise<SelectItem[]> | SelectItem[];
	actions?: ModalAction[];
	/** Shown above the list, for state that is not a row. */
	header?: () => string[];
	/** Shown when there are no rows. */
	empty?: string;
}

const listTheme = {
	selectedPrefix: (s: string) => cyan(s),
	selectedText: (s: string) => bold(s),
	description: (s: string) => dim(s),
	scrollInfo: (s: string) => dim(s),
	noMatch: (s: string) => dim(s),
};

/**
 * A titled panel wrapping a `SelectList`.
 *
 * Rendered as a bordered box because it is *modal* — it takes the keyboard, and
 * looking like part of the transcript while swallowing every key is how a UI
 * feels broken. The border is the signal that Esc is what gets you out.
 */
export class ModalView implements Component {
	private spec: ModalSpec;
	private list: SelectList;
	private status = "";
	private onClose: () => void;
	private busy = false;

	constructor(spec: ModalSpec, items: SelectItem[], onClose: () => void) {
		this.spec = spec;
		this.onClose = onClose;
		this.list = new SelectList(items, 12, listTheme);
		this.list.onCancel = () => this.onClose();
	}

	setItems(items: SelectItem[]): void {
		const previous = this.list.getSelectedItem()?.value;
		this.list = new SelectList(items, 12, listTheme);
		this.list.onCancel = () => this.onClose();
		// Keep the cursor on the same row across a refresh: deleting the third
		// rule and landing back at the top makes deleting three rules a chore.
		if (previous) {
			const at = items.findIndex((i) => i.value === previous);
			if (at >= 0) this.list.setSelectedIndex(at);
		}
	}

	setStatus(status: string): void {
		this.status = status;
	}

	invalidate(): void {
		this.list.invalidate();
	}

	handleInput(data: string): void {
		if (this.busy) return;

		// Esc closes. Checked before the list so it never means "clear the
		// filter" while the user is trying to leave.
		if (data === "\x1b") {
			this.onClose();
			return;
		}

		const action = this.spec.actions?.find((a) => a.key.toLowerCase() === data.toLowerCase());
		if (action) {
			void this.runAction(action);
			return;
		}

		this.list.handleInput(data);
	}

	private async runAction(action: ModalAction): Promise<void> {
		// Guarded: an action may reconnect a server or rewrite a settings file,
		// and holding the key down should not start five of them.
		this.busy = true;
		try {
			const message = await action.run(this.list.getSelectedItem() ?? undefined);
			this.status = message ?? "";
			if (action.closes) {
				this.onClose();
				return;
			}
			this.setItems(await this.spec.items());
		} catch (err) {
			this.status = red(`failed: ${(err as Error).message}`);
		} finally {
			this.busy = false;
		}
	}

	render(width: number): string[] {
		const inner = Math.max(10, width - 4);
		const lines: string[] = [];

		lines.push(`${bold(this.spec.title)}`);
		lines.push(gray("─".repeat(inner)));

		for (const line of this.spec.header?.() ?? []) lines.push(...fitLines([line], inner));
		if (this.spec.header?.().length) lines.push("");

		const body = this.list.render(inner);
		if (body.length === 0 || (body.length === 1 && body[0].trim() === "")) {
			lines.push(dim(this.spec.empty ?? "Nothing here."));
		} else {
			lines.push(...body);
		}

		lines.push("");
		if (this.status) lines.push(...fitLines([this.status], inner));

		// The footer is not decoration: a modal that takes the keyboard has to
		// say what the keys now do, or the only discoverable action is Esc.
		const keys = [
			...(this.spec.actions ?? []).map((a) => `${bold(a.key)} ${a.label}`),
			`${bold("esc")} close`,
		];
		lines.push(dim(keys.join("   ")));

		return lines.map((line) => fitStatus(line, width));
	}
}

export interface ModalHost {
	open: (spec: ModalSpec) => Promise<void>;
}

/** Wire a modal opener onto a TUI. Commands use this without knowing about overlays. */
export function createModalHost(tui: TUI): ModalHost {
	return {
		open: async (spec: ModalSpec) => {
			const items = await spec.items();
			let handle: { hide: () => void } | undefined;
			const view = new ModalView(spec, items, () => {
				handle?.hide();
				tui.requestRender();
			});
			handle = tui.showOverlay(view, { width: "80%", maxHeight: "80%", anchor: "center" });
			tui.requestRender();
		},
	};
}

/** Colour a status word the same way everywhere: green good, yellow warn, red bad. */
export function statusColour(ok: boolean, warn = false): (s: string) => string {
	return ok ? green : warn ? yellow : red;
}
