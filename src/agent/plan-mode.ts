import type { AgentHarnessTool } from "@earendil-works/pi-agent-core";
import type { TSchema } from "@earendil-works/pi-ai";
import type { PermissionMode } from "../claude/settings.ts";

/**
 * Plan mode.
 *
 * Read-only enforcement is only half of it. Blocking mutations gives you a
 * crippled session, not a planning session — what makes plan mode useful is the
 * *handoff*: the model researches, presents a plan, and waits for approval
 * before anything is allowed to change.
 *
 * So plan mode needs a way out, and that exit has to be an explicit, reviewable
 * step rather than the model deciding on its own that it is done thinking.
 */

export type PlanDecision =
	/** Approve and let the model proceed, leaving plan mode. */
	| { kind: "approve"; mode: PermissionMode }
	/** Keep planning; the feedback goes back to the model. */
	| { kind: "revise"; feedback: string };

/** Asks the user to approve a plan. Implemented by the TUI. */
export type PlanApprover = (plan: string) => Promise<PlanDecision>;

export interface PlanModeController {
	isActive(): boolean;
	/** Called when a plan is approved, to leave read-only mode. */
	onApprove(mode: PermissionMode): void;
}

/**
 * `exit_plan_mode` — the model's request to stop planning and start doing.
 *
 * Deliberately a tool rather than a convention like "print PLAN:". A tool call
 * is structured, unambiguous, and can block on a real answer; prose has to be
 * pattern-matched and can be produced accidentally mid-thought.
 */
export function createExitPlanModeTool<TContext extends object | undefined>(args: {
	controller: PlanModeController;
	approve: PlanApprover;
}): AgentHarnessTool<TContext> {
	return {
		name: "exit_plan_mode",
		label: "Present plan",
		description:
			"Call this when you have finished researching and have a plan ready for the user to approve. " +
			"Only for tasks that will change things — if the user asked a question or wants research, " +
			"just answer. Pass the plan as concise markdown.",
		parameters: {
			type: "object",
			properties: {
				plan: { type: "string", description: "The plan, as markdown. Be specific about what will change." },
			},
			required: ["plan"],
		} as unknown as TSchema,
		execute: async (_id: string, params: unknown) => {
			const plan = String((params as { plan?: string }).plan ?? "").trim();

			if (!args.controller.isActive()) {
				// Calling it outside plan mode is a model error, not a user-facing
				// one. Say so plainly so it stops trying.
				return {
					content: [{ type: "text" as const, text: "Not in plan mode; no approval needed. Proceed directly." }],
					details: undefined,
				};
			}

			if (!plan) {
				return {
					content: [{ type: "text" as const, text: "No plan provided. Call again with the plan text." }],
					details: undefined,
				};
			}

			const decision = await args.approve(plan);

			if (decision.kind === "approve") {
				args.controller.onApprove(decision.mode);
				return {
					content: [
						{
							type: "text" as const,
							text: `Plan approved. You may now make changes. Permission mode is "${decision.mode}". Follow the plan you presented.`,
						},
					],
					details: undefined,
				};
			}

			return {
				content: [
					{
						type: "text" as const,
						// Stays in plan mode: the point of "revise" is another round of
						// planning, not a grudging approval.
						text: `The user wants changes to the plan before proceeding: ${decision.feedback}\n\nStill in plan mode - research and present a revised plan.`,
					},
				],
				details: undefined,
			};
		},
	} as unknown as AgentHarnessTool<TContext>;
}

/**
 * Instruction appended to the system prompt while planning.
 *
 * Without this the model discovers it is read-only by hitting refusals one tool
 * at a time, which reads as a broken session rather than a deliberate mode.
 */
export const PLAN_MODE_PROMPT = [
	"You are in PLAN MODE. You may read files, search, and run read-only commands,",
	"but you must not edit, write, or run anything that changes state.",
	"",
	"Research the task thoroughly first. When you have a concrete plan, call",
	"exit_plan_mode with it and wait for approval. Do not attempt changes before",
	"the plan is approved - they will be refused.",
	"",
	"If the user only asked a question, answer it; do not present a plan.",
].join("\n");
