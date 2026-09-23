import { relative } from "node:path";
import type { Component } from "@earendil-works/pi-tui";
import type { PermissionRequest, PromptChoice } from "../claude/permission.ts";
import type { PlanDecision } from "../agent/plan-mode.ts";
import { bold, dim, gray, green, red, yellow } from "./theme.ts";
import { renderChangePreview } from "./change-preview.ts";
import { fitLines } from "./width.ts";

/**
 * Inline permission prompt.
 *
 * Parity spec §8: an inline block in the transcript flow, not a modal. That
 * matters more than it sounds — a modal hides the conversation you are being
 * asked about, which is precisely the context needed to answer.
 *
 * Number keys select. `y`/`n` are accepted too because people type them
 * reflexively, and Esc means "no" since that is the safe reading of a user who
 * backed out.
 */

export interface PendingPrompt {
	request: PermissionRequest;
	resolve: (choice: PromptChoice) => void;
}

interface PendingPlan {
	plan: string;
	resolve: (decision: PlanDecision) => void;
}

/**
 * Truncate a long argument for display without hiding what is being approved.
 *
 * Absolute paths are shortened relative to cwd: a 70-character temp path pushes
 * the part that matters off the line, and the prompt is unreadable if the thing
 * being approved does not fit on screen.
 */
function summarizeArg(request: PermissionRequest): string {
	let arg = request.primaryArg ?? "";
	if (arg.startsWith("/")) {
		const rel = relative(process.cwd(), arg);
		// Only prefer the relative form when it stays inside the project; a path
		// full of "../.." is less clear than the absolute one.
		if (rel && !rel.startsWith("..")) arg = rel;
	}
	if (arg.length <= 200) return arg;
	// Keep the head: the dangerous part of a command is almost always at the
	// front, and a tail-truncated command reads as something different.
	return `${arg.slice(0, 200)}… (${arg.length} chars)`;
}

export class PermissionPromptView implements Component {
	private pending: PendingPrompt | undefined;
	private plan: PendingPlan | undefined;
	/** Set while the user is typing feedback after choosing "no". */
	private feedback: string | undefined;

	isActive(): boolean {
		return this.pending !== undefined || this.plan !== undefined;
	}

	/** Present a plan for approval. Shares the prompt slot; only one is ever up. */
	askPlan(plan: string): Promise<PlanDecision> {
		return new Promise<PlanDecision>((resolve) => {
			this.plan = { plan, resolve };
			this.feedback = undefined;
		});
	}

	private finishPlan(decision: PlanDecision): void {
		const pending = this.plan;
		this.plan = undefined;
		this.feedback = undefined;
		pending?.resolve(decision);
	}

	ask(request: PermissionRequest): Promise<PromptChoice> {
		return new Promise<PromptChoice>((resolve) => {
			this.pending = { request, resolve };
			this.feedback = undefined;
		});
	}

	private finish(choice: PromptChoice): void {
		const pending = this.pending;
		this.pending = undefined;
		this.feedback = undefined;
		pending?.resolve(choice);
	}

	/**
	 * Returns true when the key was consumed.
	 *
	 * The editor must not also receive these keystrokes, or answering the prompt
	 * would type stray characters into the input box.
	 */
	handleKey(data: string): boolean {
		if (this.plan) return this.handlePlanKey(data);
		if (!this.pending) return false;

		// Feedback capture: everything goes into the message until Enter.
		if (this.feedback !== undefined) {
			if (data === "\r" || data === "\n") {
				this.finish({ kind: "deny", feedback: this.feedback.trim() || undefined });
				return true;
			}
			if (data === "\x7f" || data === "\b") {
				this.feedback = this.feedback.slice(0, -1);
				return true;
			}
			if (data === "\x1b") {
				this.finish({ kind: "deny" });
				return true;
			}
			// Ignore control sequences; accept printable text.
			if (data >= " ") this.feedback += data;
			return true;
		}

		switch (data.toLowerCase()) {
			case "1":
			case "y":
			case "\r":
			case "\n":
				this.finish({ kind: "allow" });
				return true;
			case "2":
			case "a":
				this.finish({ kind: "allow-always" });
				return true;
			case "3":
			case "n":
				// Collect a reason rather than refusing blankly: "no, do X
				// instead" is far more useful to the model than a bare denial.
				this.feedback = "";
				return true;
			case "\x1b":
				this.finish({ kind: "deny" });
				return true;
			default:
				// Swallow everything while the prompt is up. A stray keystroke must
				// not leak into the editor behind it.
				return true;
		}
	}

	private handlePlanKey(data: string): boolean {
		if (this.feedback !== undefined) {
			if (data === "\r" || data === "\n") {
				const text = this.feedback.trim();
				// Empty feedback is not a revision request; treat it as a cancel
				// rather than sending the model an empty instruction.
				if (text) this.finishPlan({ kind: "revise", feedback: text });
				else this.feedback = undefined;
				return true;
			}
			if (data === "\x7f" || data === "\b") {
				this.feedback = this.feedback.slice(0, -1);
				return true;
			}
			if (data === "\x1b") {
				this.feedback = undefined;
				return true;
			}
			if (data >= " ") this.feedback += data;
			return true;
		}

		switch (data.toLowerCase()) {
			case "1":
			case "y":
			case "\r":
			case "\n":
				// Approving a plan implies approving its edits; asking again for
				// every one of them defeats the point of having approved it.
				this.finishPlan({ kind: "approve", mode: "acceptEdits" });
				return true;
			case "2":
				this.finishPlan({ kind: "approve", mode: "manual" });
				return true;
			case "3":
			case "n":
			case "\x1b":
				this.feedback = "";
				return true;
			default:
				return true;
		}
	}

	invalidate(): void {}

	render(width: number): string[] {
		// Fitting happens once, here, rather than at each line-building site
		// below. Almost everything this prompt shows is content from elsewhere -
		// a bash command, a diff hunk, a model-written plan, a line the user is
		// typing - so any of it can be wider than the terminal.
		if (this.plan) return fitLines(this.renderPlan(), width, "    ");
		if (!this.pending) return [];
		const { request } = this.pending;

		const lines = [
			"",
			`${yellow("?")} ${bold("Permission required")}`,
			`  ${gray("│")} ${bold(request.toolName)}${request.primaryArg ? `(${dim(summarizeArg(request))})` : ""}`,
		];

		// The reason for the prompt changes what the answer should be, so say it.
		if (request.outsideWorkspace) {
			lines.push(`  ${gray("│")} ${yellow("outside the workspace")}`);
		}

		// Show the actual change for edits and writes. A path alone says nothing
		// about whether this is a typo fix or a file being emptied.
		const preview = renderChangePreview(request.toolName, request.args);
		if (preview) lines.push("", ...preview);
		lines.push("");

		if (this.feedback !== undefined) {
			lines.push(
				`  ${dim("What should be done instead?")}`,
				`  ${green(">")} ${this.feedback}${gray("▌")}`,
				`  ${dim("enter to send · esc to decline without a reason")}`,
			);
			return fitLines(lines, width, "    ");
		}

		lines.push(
			`  ${green("1.")} Yes`,
			`  ${green("2.")} Yes, and don't ask again for this`,
			`  ${red("3.")} No, and tell the model what to do instead`,
			"",
			`  ${dim("1-3, y/n, or esc to decline")}`,
		);
		return fitLines(lines, width, "    ");
	}
	/**
	 * Render a plan awaiting approval.
	 *
	 * The plan is shown in full rather than summarized: approving a plan you
	 * cannot read is the same failure as approving an edit you cannot see.
	 */
	private renderPlan(): string[] {
		if (!this.plan) return [];
		const lines = ["", `${yellow("?")} ${bold("Plan ready for approval")}`, ""];
		for (const line of this.plan.plan.split("\n")) lines.push(`  ${line}`);
		lines.push("");

		if (this.feedback !== undefined) {
			lines.push(
				`  ${dim("What should change about the plan?")}`,
				`  ${green(">")} ${this.feedback}${gray("\u258c")}`,
				`  ${dim("enter to send · esc to go back")}`,
			);
			return lines;
		}

		lines.push(
			`  ${green("1.")} Approve and proceed ${dim("(auto-accept edits)")}`,
			`  ${green("2.")} Approve, but confirm each change`,
			`  ${red("3.")} Keep planning, with feedback`,
			"",
			`  ${dim("1-3, y/n")}`,
		);
		return lines;
	}

}

