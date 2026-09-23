import type { AgentHarnessTool } from "@earendil-works/pi-agent-core";
import type { TSchema } from "@earendil-works/pi-ai";
import type { TodoView } from "../tui/transcript.ts";

/**
 * Todo list.
 *
 * `renderTodos` already existed in the transcript renderer with nothing
 * producing todos — this closes that gap.
 *
 * The list is session state, not conversation state: it lives here rather than
 * accumulating in the transcript. On a 32k window, re-sending the full list on
 * every update would spend the context the list exists to help manage.
 */

export class TodoStore {
	private items: TodoView[] = [];
	private listeners: Array<(items: TodoView[]) => void> = [];

	get(): TodoView[] {
		return this.items;
	}

	set(items: TodoView[]): void {
		this.items = items;
		for (const listener of this.listeners) listener(items);
	}

	onChange(listener: (items: TodoView[]) => void): void {
		this.listeners.push(listener);
	}

	/** One-line summary for the footer or a status command. */
	summary(): string {
		if (this.items.length === 0) return "no todos";
		const done = this.items.filter((t) => t.status === "completed").length;
		return `${done}/${this.items.length} done`;
	}
}

export function createTodoTool<TContext extends object | undefined>(store: TodoStore): AgentHarnessTool<TContext> {
	return {
		name: "todo_write",
		label: "Update todos",
		description:
			"Track progress on a multi-step task. Replaces the whole list each call. " +
			"Use for work with 3+ distinct steps; skip it for single actions. " +
			"Exactly one item should be in_progress at a time.",
		parameters: {
			type: "object",
			properties: {
				todos: {
					type: "array",
					items: {
						type: "object",
						required: ["content", "status"],
						properties: {
							content: { type: "string", description: "What needs doing." },
							status: { type: "string", enum: ["pending", "in_progress", "completed"] },
						},
					},
				},
			},
			required: ["todos"],
		} as unknown as TSchema,
		execute: async (_id: string, params: unknown) => {
			const todos = ((params as { todos?: TodoView[] }).todos ?? []).filter(
				(t) => typeof t?.content === "string" && typeof t?.status === "string",
			);
			store.set(todos);

			// Return a count, not the list. The model just sent it; echoing it back
			// doubles its context cost for no new information.
			const done = todos.filter((t) => t.status === "completed").length;
			const active = todos.find((t) => t.status === "in_progress");
			return {
				content: [
					{
						type: "text" as const,
						text: `${todos.length} todos, ${done} done.${active ? ` Now: ${active.content}` : ""}`,
					},
				],
				details: undefined,
			};
		},
	} as unknown as AgentHarnessTool<TContext>;
}
