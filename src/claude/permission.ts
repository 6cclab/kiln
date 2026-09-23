import { isAbsolute, relative, resolve } from "node:path";
import { decide, type Decision, type PermissionMode, type Permissions } from "./settings.ts";

/**
 * Permission gating for tool calls.
 *
 * Wired to pi's `before_tool` hook, which can return `{ block: { reason } }`.
 * The hook is async, so it can await a decision from the user without the agent
 * loop needing to know a UI exists.
 *
 * The rule that matters most: a **denial is not an error**. Blocking returns a
 * reason to the model and the turn continues, so the user can redirect rather
 * than having the whole run collapse. That is why rejection carries feedback.
 */

export type PromptChoice =
	| { kind: "allow" }
	/** Allow, and stop asking for this tool (or this exact command) in this session. */
	| { kind: "allow-always" }
	/** Refuse, and tell the model what to do instead. */
	| { kind: "deny"; feedback?: string };

export interface PermissionRequest {
	toolName: string;
	/** Set when the target path lies outside the workspace roots. */
	outsideWorkspace?: boolean;
	/** The identifying argument, e.g. the command or file path. */
	primaryArg?: string;
	/** Full arguments, for rendering a diff or the exact command. */
	args: Record<string, unknown>;
}

/** Asks the user. Implemented by the TUI; absent in headless runs. */
export type PermissionPrompter = (request: PermissionRequest) => Promise<PromptChoice>;

export interface GateOptions {
	permissions: Permissions;
	/** Directories tools may touch without asking. Defaults to [cwd]. */
	roots?: string[];
	mode?: PermissionMode;
	prompt?: PermissionPrompter;
}

export interface BlockResult {
	reason: string;
}

export class PermissionGate {
	private permissions: Permissions;
	private prompter?: PermissionPrompter;
	mode: PermissionMode;

	/**
	 * Grants added by "yes, don't ask again", scoped to this session only.
	 *
	 * Deliberately not persisted to `.claude/settings.json`: a permission granted
	 * in a hurry to unblock one task should not silently become permanent policy.
	 * Persisting is a separate, explicit act.
	 */
	private sessionAllows = new Set<string>();

	/**
	 * Everything refused this session.
	 *
	 * Print mode reports this: a script that cannot tell whether a step was
	 * refused will treat a partial run as a success.
	 */
	private blockLog: string[] = [];

	/**
	 * Directories tools may operate in freely.
	 *
	 * This is a real boundary, not bookkeeping: `NodeExecutionEnv` resolves
	 * relative paths against cwd but does NOT sandbox absolute ones, so without
	 * this check a model could read `~/.ssh/id_rsa` under an `allow: [Read]`
	 * rule that the user only ever meant to apply to their project.
	 */
	private roots: string[];

	constructor(opts: GateOptions) {
		this.roots = (opts.roots ?? [process.cwd()]).map((r) => resolve(r));
		this.permissions = opts.permissions;
		this.mode = opts.mode ?? opts.permissions.defaultMode ?? "manual";
		this.prompter = opts.prompt;
	}

	/** Widen the workspace. Returns the resolved path actually added. */
	addRoot(dir: string): string {
		const full = resolve(dir);
		if (!this.roots.includes(full)) this.roots.push(full);
		return full;
	}

	getRoots(): readonly string[] {
		return this.roots;
	}

	/** True when a path lies inside any allowed root. */
	private withinRoots(path: string): boolean {
		const full = isAbsolute(path) ? path : resolve(this.roots[0] ?? process.cwd(), path);
		return this.roots.some((root) => {
			const rel = relative(root, full);
			// Empty means the path IS the root; a leading ".." means it escapes.
			return rel === "" || (!rel.startsWith("..") && !isAbsolute(rel));
		});
	}

	setMode(mode: PermissionMode): void {
		this.mode = mode;
	}

	/**
	 * Bind the UI that asks the user.
	 *
	 * The gate is constructed before the TUI (the session needs it to build the
	 * before_tool hook), so the prompter arrives later rather than at construction.
	 */
	setPrompter(prompter: PermissionPrompter): void {
		this.prompter = prompter;
	}

	/** Refusals so far, for reporting. */
	/** The merged rules, for `/permissions`. */
	getPermissions(): Permissions {
		return this.permissions;
	}

	/**
	 * Add a rule for the rest of this session.
	 *
	 * Separate from persisting it. The in-memory set is what the next tool call
	 * is judged against, and a rule that only reached disk would not apply until
	 * a restart — which, for someone editing rules mid-task to unblock
	 * themselves, is the same as it not working.
	 */
	addRule(list: "allow" | "deny" | "ask", rule: string): void {
		if (!this.permissions[list].includes(rule)) this.permissions[list].push(rule);
	}

	removeRule(list: "allow" | "deny" | "ask", rule: string): void {
		this.permissions[list] = this.permissions[list].filter((r) => r !== rule);
	}

	/**
	 * Grants made by "yes, don't ask again" this session.
	 *
	 * Surfaced because they are invisible otherwise: they are not in any file,
	 * and a session that has quietly accumulated ten of them looks identical to
	 * one that has none.
	 */
	getSessionGrants(): string[] {
		return [...this.sessionAllows];
	}

	getBlocked(): readonly string[] {
		return this.blockLog;
	}

	private record(request: PermissionRequest, reason: string): BlockResult {
		this.blockLog.push(`${request.toolName}(${request.primaryArg ?? ""}): ${reason}`);
		return { reason };
	}

	/** Key a session grant by tool plus argument, so "always" is not a blank cheque. */
	private key(toolName: string, primaryArg?: string): string {
		return `${toolName}::${primaryArg ?? ""}`;
	}

	/**
	 * Decide, prompting if necessary.
	 *
	 * Returns `undefined` to proceed, or a block reason. The reason is written
	 * for the model, not the user - it is what the model reads to understand why
	 * the call failed and what to do instead.
	 */
	async check(request: PermissionRequest): Promise<BlockResult | undefined> {
		if (this.sessionAllows.has(this.key(request.toolName, request.primaryArg))) return undefined;

		const verdict: Decision = decide(this.permissions, request.toolName, request.primaryArg, this.mode);

		// A path outside the workspace always warrants a question, even when a
		// rule would otherwise allow the tool. `allow: [Read]` means "reading is
		// fine here", not "read anything on this machine".
		const path = pathArgOf(request.args);
		const escaped = path !== undefined && !this.withinRoots(path);
		if (escaped && verdict === "allow" && this.mode !== "bypassPermissions") {
			if (!this.prompter) {
				return this.record(request, `${path} is outside the workspace and cannot be confirmed.`);
			}
			const choice = await this.prompter({ ...request, outsideWorkspace: true });
			if (choice.kind === "deny") {
				return this.record(request, "the user declined access to a path outside the workspace.");
			}
			if (choice.kind === "allow-always") this.sessionAllows.add(this.key(request.toolName, request.primaryArg));
			return undefined;
		}

		if (verdict === "allow") return undefined;
		if (verdict === "deny") {
			return this.record(
				request,
				this.mode === "plan"
					? `plan mode is read-only, so ${request.toolName} is not available. Describe the change instead of making it.`
					: "blocked by permission rules.",
			);
		}

		// verdict === "ask"
		if (!this.prompter) {
			// Headless with no way to ask. Refusing beats proceeding: an
			// unattended run must not silently take an action the policy said
			// required confirmation.
			return this.record(request, "requires confirmation and no prompt is available.");
		}

		const choice = await this.prompter(request);
		if (choice.kind === "allow") return undefined;
		if (choice.kind === "allow-always") {
			this.sessionAllows.add(this.key(request.toolName, request.primaryArg));
			return undefined;
		}

		return this.record(
			request,
			choice.feedback
				? `the user declined and said: ${choice.feedback}`
				: "the user declined. Ask what they would prefer before trying again.",
		);
	}
}

/**
 * The identifying argument for a call, mirroring what the transcript shows.
 *
 * Permission rules match on this, so it must agree with the renderer: a user
 * approving `Bash(git status)` should be approving exactly what they were shown.
 */
/** The path a call targets, if any. Used for the workspace-boundary check. */
export function pathArgOf(args: Record<string, unknown>): string | undefined {
	for (const key of ["path", "file_path", "filePath"]) {
		const value = args[key];
		if (typeof value === "string") return value;
	}
	return undefined;
}

export function primaryArgOf(args: Record<string, unknown>): string | undefined {
	for (const key of ["command", "path", "file_path", "filePath", "pattern", "query", "url"]) {
		const value = args[key];
		if (typeof value === "string") return value;
	}
	return undefined;
}
