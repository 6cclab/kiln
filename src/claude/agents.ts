import { readdir, readFile } from "node:fs/promises";
import { basename, join } from "node:path";
import { parse as parseYaml } from "yaml";
import { claudeRoots } from "./paths.ts";

/**
 * `.claude/agents/*.md` — subagent definitions.
 *
 * A subagent is a fresh agent with its own context window, its own system
 * prompt, and usually a narrower tool set. The parent dispatches a task and
 * gets back only the final answer.
 *
 * The context isolation is the whole point, and it is worth being precise about
 * why it matters more here than in Claude Code. A subagent that reads thirty
 * files to answer one question spends thirty files' worth of tokens — in *its*
 * window, not the parent's. The parent pays for one paragraph. On a 32k model
 * that is the difference between a task being possible and not.
 *
 * ## Format
 *
 * Read from real definitions in this user's projects rather than from memory:
 *
 * ```yaml
 * name: k8s-infra                 # required
 * description: Kubernetes ...     # required - the model reads this to choose
 * model: sonnet                   # optional
 * tools: Read, Glob, Grep         # optional allowlist
 * color: green                    # ignored
 * role: ...                       # ignored (non-standard, seen in the wild)
 * paths: [cluster/**]             # ignored
 * skills: [argocd, kustomize]     # ignored
 * ```
 *
 * Unknown keys are kept rather than rejected: they are how people annotate
 * their own definitions, and a loader that refuses a file over an extra key
 * breaks a directory it does not own.
 */

export interface AgentDefinition {
	name: string;
	description: string;
	/** The system prompt: everything after the frontmatter. */
	prompt: string;
	/**
	 * Requested model, verbatim from the file. An alias like `sonnet` is a
	 * hint, not a requirement - see `resolveAgentModel`.
	 */
	model?: string;
	/** Tool allowlist. `undefined` means "inherit the parent's tools". */
	tools?: string[];
	source: "personal" | "project";
	path: string;
}

interface AgentFrontmatter {
	name?: string;
	description?: string;
	model?: string;
	tools?: string | string[] | null;
}

/** `Read, Glob, Grep` or a YAML list. Both forms appear in real files. */
function parseTools(value: string | string[] | null | undefined): string[] | undefined {
	// A bare `tools:` with no value parses to null, not undefined. Left
	// unguarded this throws, and the throw takes the whole agent with it.
	if (value === undefined || value === null) return undefined;
	const list = Array.isArray(value) ? value : String(value).split(",");
	const cleaned = list.map((t) => String(t).trim()).filter(Boolean);
	return cleaned.length > 0 ? cleaned : undefined;
}

export function parseAgent(source: string, path: string, scope: "personal" | "project"): AgentDefinition | undefined {
	const match = source.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n?([\s\S]*)$/);
	if (!match) return undefined;

	let data: AgentFrontmatter;
	try {
		data = (parseYaml(match[1]) ?? {}) as AgentFrontmatter;
	} catch {
		// Unlike a command, an agent with no frontmatter has no name to be
		// dispatched by and no description for the model to choose it from.
		// There is nothing usable to salvage.
		return undefined;
	}

	// `name` may be omitted; the filename is what Claude Code falls back to and
	// what most definitions match anyway.
	const name = String(data.name ?? basename(path, ".md")).trim();
	const description = String(data.description ?? "").trim();
	if (!name || !description) return undefined;

	return {
		name,
		description,
		prompt: match[2].trim(),
		model: data.model ? String(data.model).trim() : undefined,
		tools: parseTools(data.tools),
		source: scope,
		path,
	};
}

async function loadFrom(dir: string, scope: "personal" | "project"): Promise<AgentDefinition[]> {
	let entries: string[];
	try {
		entries = await readdir(dir);
	} catch {
		// No agents directory is the common case, not an error.
		return [];
	}

	const out: AgentDefinition[] = [];
	for (const entry of entries) {
		if (!entry.endsWith(".md")) continue;
		const path = join(dir, entry);
		try {
			const agent = parseAgent(await readFile(path, "utf8"), path, scope);
			if (agent) out.push(agent);
		} catch {
			// One unreadable file must not cost the others.
		}
	}
	return out;
}

/**
 * Load every agent definition, project shadowing personal.
 *
 * Same precedence as commands and settings: a project may redefine an agent
 * name for its own repo without editing the user's global directory.
 */
export async function loadAgents(cwd: string): Promise<AgentDefinition[]> {
	const byName = new Map<string, AgentDefinition>();
	for (const root of claudeRoots(cwd)) {
		const scope = root.scope === "user" ? ("personal" as const) : ("project" as const);
		for (const agent of await loadFrom(join(root.dir, "agents"), scope)) {
			byName.set(agent.name, agent);
		}
	}
	return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
}

export interface ModelChoice {
	providerId: string;
	modelId: string;
}

/**
 * Resolve an agent's `model:` field against what this harness is actually
 * running on.
 *
 * `sonnet` / `opus` / `haiku` are Anthropic names. In a model-agnostic harness
 * they cannot be requirements, because on a self-hosted Ollama session there is
 * no Sonnet to dispatch to and failing the task over it would be absurd. So
 * they are treated as **hints**:
 *
 *   - `provider/model` is explicit and resolved literally.
 *   - A bare alias is honored only if the *parent's own provider* offers a
 *     matching model. On Anthropic, `model: sonnet` does what the file says.
 *     On Ollama it quietly inherits the parent's model.
 *   - `inherit`, or anything unresolvable, inherits.
 *
 * Returning the parent's model rather than throwing is deliberate: a definition
 * written for one machine should still run on another. The caller reports what
 * was actually used, so an inherited model is visible rather than silent.
 */
export function resolveAgentModel(
	requested: string | undefined,
	parent: ModelChoice,
	candidates: ReadonlyArray<{ id: string; provider: string }>,
): ModelChoice {
	if (!requested || requested === "inherit") return parent;

	const slash = requested.indexOf("/");
	if (slash !== -1) {
		const providerId = requested.slice(0, slash);
		const modelId = requested.slice(slash + 1);
		// Only if it actually exists; a typo must not take down the dispatch.
		const found = candidates.find((c) => c.provider === providerId && c.id === modelId);
		return found ? { providerId, modelId } : parent;
	}

	// A bare alias is scoped to the parent's provider on purpose. Matching it
	// across every configured provider would silently move a task onto a paid
	// API because a local definition happened to say "opus".
	const alias = requested.toLowerCase();
	const match = candidates.find((c) => c.provider === parent.providerId && c.id.toLowerCase().includes(alias));
	return match ? { providerId: match.provider, modelId: match.id } : parent;
}
