import { join } from "node:path";
import { BACKGROUND_CONTEXT, loadSourcedSkills } from "@earendil-works/pi-agent-core";
import type { ExecutionEnv, Skill } from "@earendil-works/pi-agent-core";
import type { Command, CommandSource } from "../commands/registry.ts";
import { claudeRoots, type Scope } from "./paths.ts";

/**
 * Claude skills.
 *
 * pi's `loadSkills` already parses Claude's exact `SKILL.md` format — recursive
 * discovery, `name`/`description` frontmatter, ignore-file aware — so this is
 * directory wiring, not a parser.
 *
 * Skills serve two masters, which is the whole reason they live in one
 * namespace with commands:
 *
 *   1. **Model-visible resources.** Only `name` + `description` stay resident;
 *      the body is injected on invocation. That is what makes a large skill
 *      library affordable inside a 32k window.
 *   2. **User-invocable slash commands.** `/golang-security` runs the skill
 *      directly. Claude marks these with `user-invocable: true`.
 */

export interface SourcedSkill extends Skill {
	scope: Scope;
	/** `user-invocable: true` in frontmatter. Controls slash-command exposure. */
	userInvocable: boolean;
}

/**
 * pi's `Skill` drops unrecognized frontmatter keys, and `user-invocable` is one
 * of them, so it is re-read from the file's own header.
 *
 * Default is **true**: every skill in `~/.claude/skills` was observed to set it,
 * and a skill that silently fails to appear in the palette is a worse failure
 * than one that appears when it need not.
 */
function readUserInvocable(content: string): boolean {
	const header = content.match(/^---\r?\n([\s\S]*?)\r?\n---/);
	if (!header) return true;
	const flag = header[1].match(/^user-invocable:\s*(\S+)/m);
	return flag ? flag[1] !== "false" : true;
}

export async function loadClaudeSkills(env: ExecutionEnv, cwd: string): Promise<SourcedSkill[]> {
	const inputs = claudeRoots(cwd).map((root) => ({ path: join(root.dir, "skills"), source: root.scope }));

	const { skills } = await loadSourcedSkills<Scope, SourcedSkill>(
		env,
		inputs,
		(skill, scope) => ({
			...skill,
			scope,
			userInvocable: readUserInvocable(skill.content),
		}),
		BACKGROUND_CONTEXT,
	);

	// Later scopes win: a project skill shadows a personal one of the same name.
	// `loadSourcedSkills` returns { skill, source } pairs, not bare skills.
	const byName = new Map<string, SourcedSkill>();
	for (const { skill } of skills) byName.set(skill.name, skill);
	return [...byName.values()];
}

/**
 * Expose user-invocable skills as slash commands.
 *
 * Invoking one returns a prompt rather than output — the skill body becomes the
 * model's instructions, which is exactly how `.claude/commands` behave, so both
 * flow through the same `CommandResult.prompt` path.
 */
export function skillCommandSource(
	skills: SourcedSkill[],
	invoke: (name: string, args: string) => Promise<string>,
): CommandSource {
	return {
		origin: "skill",
		load: (): Command[] =>
			skills
				.filter((skill) => skill.userInvocable)
				.map((skill) => ({
					name: skill.name,
					origin: "skill" as const,
					description: skill.description,
					argumentHint: "[instructions]",
					run: async ({ args }) => ({ prompt: await invoke(skill.name, args) }),
				})),
	};
}
