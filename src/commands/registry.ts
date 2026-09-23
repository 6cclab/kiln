import type { AutocompleteItem, SlashCommand } from "@earendil-works/pi-tui";

/**
 * The slash command registry.
 *
 * Built from one observation: in Claude Code, built-ins, project commands,
 * plugin commands and *skills* all resolve in a single flat namespace. That is
 * why `--disable-slash-commands` is documented as "Disable all skills" — they
 * are not separate systems with separate lookup paths.
 *
 * So this is one registry with pluggable sources rather than a built-in list
 * that later phases patch. Phase 4 adds `.claude/commands` and skills by
 * registering a source; it does not touch this file.
 *
 * `Command` extends pi-tui's `SlashCommand` so the palette can consume the
 * registry directly, with no adapter.
 */

/** Where a command came from. Shown in the palette so origin is never ambiguous. */
export type CommandOrigin = "builtin" | "project" | "personal" | "plugin" | "skill";

export interface CommandContext {
	/** Raw argument string after the command name, trimmed. May be empty. */
	args: string;
}

export interface CommandResult {
	/** Text to display in the transcript, if any. */
	output?: string;
	/** Text to send to the model as a prompt, if the command expands to one. */
	prompt?: string;
	/**
	 * A panel to open instead of printing.
	 *
	 * Commands that MANAGE things rather than report them need somewhere to
	 * navigate and act; a listing in the transcript answers "what is
	 * configured" and leaves you to go and edit a file for the rest.
	 *
	 * Typed as unknown here on purpose: the registry has no business importing
	 * the TUI, and a command that returns one in print mode should degrade to
	 * its text output rather than fail.
	 */
	modal?: unknown;
}

export interface Command extends SlashCommand {
	name: string;
	description?: string;
	/** Placeholder shown after the name, e.g. `<provider>`. Matches Claude's `argument-hint`. */
	argumentHint?: string;
	origin: CommandOrigin;
	/** Namespace for plugin commands, rendered as `namespace:name`. */
	namespace?: string;
	run(ctx: CommandContext): Promise<CommandResult> | CommandResult;
}

/** A pluggable provider of commands. Phase 4 adds file- and skill-backed ones. */
export interface CommandSource {
	origin: CommandOrigin;
	/** Re-read on demand so edits to `.claude/commands` appear without a restart. */
	load(): Promise<Command[]> | Command[];
}

/** Fully-qualified name: `namespace:name` for plugins, bare `name` otherwise. */
export function qualifiedName(cmd: Pick<Command, "name" | "namespace">): string {
	return cmd.namespace ? `${cmd.namespace}:${cmd.name}` : cmd.name;
}

export class CommandRegistry {
	private sources: CommandSource[] = [];
	private cache: Command[] | null = null;

	addSource(source: CommandSource): void {
		this.sources.push(source);
		this.cache = null;
	}

	/** Drop the cache so the next list() re-reads every source. */
	invalidate(): void {
		this.cache = null;
	}

	async list(): Promise<Command[]> {
		if (this.cache) return this.cache;

		const loaded = await Promise.all(
			this.sources.map(async (s) => {
				try {
					return await s.load();
				} catch {
					// A malformed file in `.claude/commands` must not take down the
					// whole palette. The bad command is simply absent; every other
					// command still works.
					return [] as Command[];
				}
			}),
		);

		// Later sources win on a name collision. Sources are registered
		// least-specific first (builtin, then personal, then project), so a
		// project command deliberately shadows a built-in of the same name —
		// matching how the settings hierarchy resolves.
		const byName = new Map<string, Command>();
		for (const cmd of loaded.flat()) byName.set(qualifiedName(cmd), cmd);

		this.cache = [...byName.values()].sort((a, b) => qualifiedName(a).localeCompare(qualifiedName(b)));
		return this.cache;
	}

	async get(name: string): Promise<Command | undefined> {
		const wanted = name.replace(/^\//, "");
		return (await this.list()).find((c) => qualifiedName(c) === wanted);
	}

	/**
	 * Parse and run a line of input beginning with `/`.
	 *
	 * Returns undefined when the line is not a command, so the caller can send
	 * it to the model instead. An unknown `/word` IS treated as a failed command
	 * rather than passed through as a prompt — silently sending `/compcat fix
	 * this` to the model as prose is far more confusing than an error.
	 */
	async execute(line: string): Promise<CommandResult | undefined> {
		if (!line.startsWith("/")) return undefined;

		// Collapse repeated leading slashes. A completion bug once turned "/" +
		// "/model" into "//model", which fell through to the model as a prompt
		// and burned a full local-model turn before failing confusingly. Anything
		// starting with "/" is a command attempt and must be handled as one.
		const match = line.replace(/^\/+/, "").match(/^([A-Za-z0-9_:-]+)\s*([\s\S]*)$/);
		if (!match) return { output: `Not a valid command: ${line}` };

		const [, name, rest] = match;
		const cmd = await this.get(name);
		if (!cmd) {
			return { output: `Unknown command: /${name}. Type / to see what is available.` };
		}
		return cmd.run({ args: rest.trim() });
	}

	/** Palette rows for pi-tui's autocomplete provider. */
	async toAutocompleteItems(): Promise<AutocompleteItem[]> {
		return (await this.list()).map((c) => ({
			value: `/${qualifiedName(c)}`,
			// The hint renders inline after the name, as Claude Code does for
			// commands that take arguments.
			label: c.argumentHint ? `/${qualifiedName(c)} ${c.argumentHint}` : `/${qualifiedName(c)}`,
			description: c.description,
		}));
	}
}

/** Convenience source for a fixed list, used by the built-ins. */
export function staticSource(origin: CommandOrigin, commands: Omit<Command, "origin">[]): CommandSource {
	return {
		origin,
		load: () => commands.map((c) => ({ ...c, origin })),
	};
}
