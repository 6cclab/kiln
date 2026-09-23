import type { CommandSource } from "./registry.ts";
import type { Registry } from "../provider/registry.ts";
import type { TodoStore } from "../agent/todo.ts";
import type { Tier } from "../budget/tier.ts";
import { renderTodos } from "../tui/transcript.ts";
import { usableTokens } from "../budget/tier.ts";

/**
 * The remaining parity commands: `/login`, `/logout`, `/usage`, `/todos`,
 * `/terminal-setup`.
 *
 * Two of these are adaptations rather than copies, and the difference is worth
 * stating because copying them literally would produce something misleading:
 *
 *   - **`/usage`** in Claude Code reports plan limits. This harness has no
 *     plan; on a self-hosted model there is nothing to meter. What actually
 *     constrains a session here is the context budget, so that is what it
 *     reports. A `/usage` that printed "unlimited" would be true and useless.
 *   - **`/login` and `/logout`** already exist as CLI subcommands. As slash
 *     commands they report status and point at the subcommand rather than
 *     running an OAuth flow inline — the flow needs to open a browser and bind
 *     a local port, which is not something to start from inside a turn.
 *
 * `/vim`, `/statusline` and `/plugin` are deliberately ABSENT rather than
 * present-and-apologetic. A command that exists only to say "not implemented"
 * is noise in `/help` on every session, and pretends the checklist matters more
 * than the tool working. An unknown command already produces a clear error.
 */

export interface AccountDeps {
	registry: Registry;
	todos: TodoStore;
	tier: Tier;
	modelLabel: string;
	/** Tokens currently resident, when known. */
	contextUsed?: () => number | undefined;
}

export function accountCommands(deps: AccountDeps): CommandSource {
	return {
		origin: "builtin",
		load: async () => [
			{
				origin: "builtin" as const,
				name: "login",
				description: "Show auth status, or how to log in to a provider",
				argumentHint: "[provider]",
				getArgumentCompletions: (prefix: string) =>
					deps.registry.models
						.getProviders()
						.filter((p) => p.auth && p.id.startsWith(prefix.trim()))
						.map((p) => ({
							value: p.id,
							label: p.id,
							description: p.auth?.oauth?.isSubscription ? "subscription" : "api key",
						})),
				run: async (ctx) => {
					const wanted = ctx.args.trim();
					if (wanted) {
						// Deliberately not run inline: the OAuth flow opens a browser
						// and binds a local callback port, which must not happen in the
						// middle of a turn.
						return { output: `Run this outside the session:\n\n  harness login ${wanted}` };
					}
					const providers = deps.registry.models.getProviders();
					const lines: string[] = [];
					for (const p of providers) {
						if (!p.auth) continue;
						const check = await deps.registry.models.checkAuth(p.id);
						if (!check) continue;
						const subscription = p.auth.oauth?.isSubscription ? " (subscription)" : "";
						lines.push(`  ${p.id.padEnd(18)} ${check.type}${subscription}`);
					}
					return {
						output:
							lines.length > 0
								? ["logged in:", ...lines, "", "Add one with: harness login <provider>"].join("\n")
								: "Not logged in to any provider.\n\nLog in with: harness login <provider>",
					};
				},
			},
			{
				origin: "builtin" as const,
				name: "logout",
				description: "How to log out of a provider",
				argumentHint: "<provider>",
				// Only providers actually logged in: offering the other forty is a
				// list to scroll past, not a choice.
				getArgumentCompletions: async (prefix: string) => {
					const out: Array<{ value: string; label: string; description: string }> = [];
					for (const p of deps.registry.models.getProviders()) {
						if (!p.id.startsWith(prefix.trim())) continue;
						const check = await deps.registry.models.checkAuth(p.id);
						if (check) out.push({ value: p.id, label: p.id, description: check.type });
					}
					return out;
				},
				run: async (ctx) => {
					const wanted = ctx.args.trim();
					if (!wanted) return { output: "usage: /logout <provider>" };
					// Not done inline: dropping the credential mid-session would leave
					// the running conversation on a model it can no longer reach.
					return { output: `Run this outside the session:\n\n  harness logout ${wanted}` };
				},
			},
			{
				origin: "builtin" as const,
				name: "usage",
				description: "Show context budget use for this session",
				run: async () => {
					const budget = usableTokens(deps.tier);
					const used = deps.contextUsed?.();
					const lines = [
						`model      ${deps.modelLabel}`,
						`tier       ${deps.tier.name}`,
						`window     ${deps.tier.contextWindow.toLocaleString()} tokens`,
						`budget     ${budget.toLocaleString()} usable after reserves`,
						`tools      ${deps.tier.toolStrategy}`,
						`per result ${deps.tier.toolOutputTokens.toLocaleString()} token ceiling`,
					];
					if (used !== undefined) {
						const percent = Math.round((used / budget) * 100);
						lines.push(`used       ${used.toLocaleString()} (${percent}% of budget)`);
					}
					// Said plainly rather than left to be inferred from a missing line.
					lines.push("", "No plan limits apply: usage here is context, not billing.");
					return { output: lines.join("\n") };
				},
			},
			{
				origin: "builtin" as const,
				name: "todos",
				description: "Show the current todo list",
				run: async () => {
					const items = deps.todos.get();
					if (items.length === 0) return { output: "No todos." };
					return { output: renderTodos(items).join("\n") };
				},
			},
			{
				origin: "builtin" as const,
				name: "terminal-setup",
				description: "Check terminal capabilities",
				run: async () => {
					// Reported rather than configured. Claude Code's version installs
					// a Shift+Enter keybinding for specific terminals; what is useful
					// here is knowing whether the TUI's assumptions hold at all.
					const term = process.env.TERM ?? "unknown";
					const program = process.env.TERM_PROGRAM ?? "unknown";
					const colors = process.env.COLORTERM ?? "not set";
					const lines = [
						`TERM          ${term}`,
						`TERM_PROGRAM  ${program}`,
						`COLORTERM     ${colors}`,
						`tty           ${process.stdout.isTTY ? "yes" : "no"}`,
						`size          ${process.stdout.columns ?? "?"}x${process.stdout.rows ?? "?"}`,
					];
					const problems: string[] = [];
					if (!process.stdout.isTTY) problems.push("not a tty - the interactive TUI cannot run here");
					if (term === "dumb") problems.push('TERM is "dumb" - use --ax-screen-reader for flat output');
					lines.push("", problems.length === 0 ? "Terminal looks fine." : problems.map((p) => `- ${p}`).join("\n"));
					return { output: lines.join("\n") };
				},
			},
		],
	};
}
