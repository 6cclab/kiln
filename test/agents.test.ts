import { strict as assert } from "node:assert";
import { describe, it, before, after } from "node:test";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { loadAgents, parseAgent, resolveAgentModel } from "../src/claude/agents.ts";
import { allowedToolNames, describeAgents, createTaskTool, GENERAL_PURPOSE } from "../src/agent/subagent.ts";
import { tierForWindow } from "../src/budget/tier.ts";

const SMALL = tierForWindow(32_768);
const LARGE = tierForWindow(200_000);

describe("parseAgent", () => {
	it("reads the fields real definitions actually use", () => {
		// Shape taken from ~/projects/devops/homelab/.claude/agents/k8s-infra.md.
		const agent = parseAgent(
			[
				"---",
				"name: k8s-infra",
				"description: Kubernetes cluster infrastructure",
				"model: sonnet",
				"tools: Read, Glob, Grep",
				"color: green",
				"paths: [cluster/**]",
				"---",
				"",
				"You are a Kubernetes expert.",
			].join("\n"),
			"/x/k8s-infra.md",
			"project",
		);
		assert.equal(agent?.name, "k8s-infra");
		assert.equal(agent?.model, "sonnet");
		assert.deepEqual(agent?.tools, ["Read", "Glob", "Grep"]);
		assert.equal(agent?.prompt, "You are a Kubernetes expert.");
	});

	it("keeps a definition that carries unknown keys", () => {
		// `role`, `paths`, `skills` and `color` all appear in real files and are
		// not Claude Code fields. Refusing over them would break a directory
		// this harness does not own.
		const agent = parseAgent("---\nname: a\ndescription: d\nrole: x\nskills: [y]\n---\nbody", "/x/a.md", "project");
		assert.equal(agent?.name, "a");
	});

	it("accepts a YAML list for tools as well as the comma form", () => {
		const agent = parseAgent("---\nname: a\ndescription: d\ntools:\n  - Read\n  - Grep\n---\nb", "/x/a.md", "project");
		assert.deepEqual(agent?.tools, ["Read", "Grep"]);
	});

	it("falls back to the filename when name is omitted", () => {
		const agent = parseAgent("---\ndescription: d\n---\nbody", "/x/from-file.md", "project");
		assert.equal(agent?.name, "from-file");
	});

	it("rejects a file with no description, since nothing could dispatch to it", () => {
		assert.equal(parseAgent("---\nname: a\n---\nbody", "/x/a.md", "project"), undefined);
		assert.equal(parseAgent("no frontmatter at all", "/x/a.md", "project"), undefined);
	});

	it("treats tools as absent rather than empty when the list is blank", () => {
		// `undefined` means inherit; `[]` would mean a tool-less agent.
		assert.equal(parseAgent("---\nname: a\ndescription: d\ntools:\n---\nb", "/x/a.md", "project")?.tools, undefined);
	});
});

describe("loadAgents", () => {
	let dir = "";

	before(async () => {
		dir = await mkdtemp(join(tmpdir(), "harness-agents-"));
		await mkdir(join(dir, ".claude", "agents"), { recursive: true });
		await writeFile(join(dir, ".claude", "agents", "a.md"), "---\nname: a\ndescription: first\n---\nbody a");
		await writeFile(join(dir, ".claude", "agents", "b.md"), "---\nname: b\ndescription: second\n---\nbody b");
		await writeFile(join(dir, ".claude", "agents", "broken.md"), "not an agent");
		await writeFile(join(dir, ".claude", "agents", "notes.txt"), "ignored");
	});

	after(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("loads the valid definitions and skips the rest", async () => {
		const agents = await loadAgents(dir);
		const names = agents.map((a) => a.name);
		assert.ok(names.includes("a") && names.includes("b"));
		assert.ok(!names.includes("broken"), "a malformed file became an agent");
		assert.ok(!names.includes("notes"), "a non-markdown file became an agent");
	});

	it("returns nothing for a directory with no agents", async () => {
		const empty = await mkdtemp(join(tmpdir(), "harness-noagents-"));
		// Personal agents may exist on this machine; only project ones are asserted on.
		const agents = (await loadAgents(empty)).filter((a) => a.source === "project");
		assert.deepEqual(agents, []);
		await rm(empty, { recursive: true, force: true });
	});
});

describe("resolveAgentModel", () => {
	const ollama = [
		{ id: "qwen3.8:latest", provider: "ollama" },
		{ id: "qwen3-cc:latest", provider: "ollama" },
	];
	const anthropic = [
		{ id: "claude-sonnet-4-5", provider: "anthropic" },
		{ id: "claude-opus-4-1", provider: "anthropic" },
	];
	const localParent = { providerId: "ollama", modelId: "qwen3.8:latest" };

	it("honors a bare alias when the parent's provider has a match", () => {
		const out = resolveAgentModel("sonnet", { providerId: "anthropic", modelId: "claude-opus-4-1" }, anthropic);
		assert.deepEqual(out, { providerId: "anthropic", modelId: "claude-sonnet-4-5" });
	});

	it("inherits when the alias means nothing on this provider", () => {
		// The motivating case: every definition on this machine says
		// `model: sonnet`, and there is no Sonnet on a self-hosted Ollama.
		// Failing the dispatch over it would be absurd.
		assert.deepEqual(resolveAgentModel("sonnet", localParent, ollama), localParent);
	});

	it("does not cross providers for a bare alias", () => {
		// Matching "opus" against every configured provider would silently move
		// a local task onto a paid API.
		assert.deepEqual(resolveAgentModel("opus", localParent, [...ollama, ...anthropic]), localParent);
	});

	it("resolves an explicit provider/model literally", () => {
		assert.deepEqual(resolveAgentModel("ollama/qwen3-cc:latest", localParent, ollama), {
			providerId: "ollama",
			modelId: "qwen3-cc:latest",
		});
	});

	it("inherits rather than failing on a typo", () => {
		assert.deepEqual(resolveAgentModel("ollama/nope", localParent, ollama), localParent);
		assert.deepEqual(resolveAgentModel("inherit", localParent, ollama), localParent);
		assert.deepEqual(resolveAgentModel(undefined, localParent, ollama), localParent);
	});
});

describe("allowedToolNames", () => {
	const available = ["bash", "read", "edit", "write", "task", "mcp__x__y"];

	it("matches Claude Code's casing against pi's lowercase tools", () => {
		assert.deepEqual(allowedToolNames(["Read", "Bash"], available), ["bash", "read"]);
	});

	it("never grants task, so a subagent cannot dispatch further", () => {
		// Recursive dispatch turns one runaway agent into a fork bomb.
		assert.ok(!allowedToolNames(undefined, available).includes("task"));
		assert.ok(!allowedToolNames(["task", "Read"], available).includes("task"));
	});

	it("inherits everything when no allowlist is given", () => {
		assert.deepEqual(allowedToolNames(undefined, available), ["bash", "read", "edit", "write", "mcp__x__y"]);
	});

	it("falls back to everything when an allowlist matches nothing", () => {
		// Far more likely a naming mismatch than a genuine request for an agent
		// with no tools, and a tool-less agent cannot research at all.
		assert.deepEqual(allowedToolNames(["Glob", "WebFetch"], available), [
			"bash",
			"read",
			"edit",
			"write",
			"mcp__x__y",
		]);
	});
});

describe("describeAgents", () => {
	const many = Array.from({ length: 13 }, (_, i) => ({
		...GENERAL_PURPOSE,
		name: `agent-${i}`,
		description: "x".repeat(400),
	}));

	it("clips descriptions on the small tier to protect the tool budget", () => {
		const small = describeAgents(many, SMALL);
		const large = describeAgents(many, LARGE);
		assert.ok(small.length < large.length, "small tier was not cheaper");
		assert.ok(small.includes("..."), "nothing was clipped");
		// Every agent must still be dispatchable: a clipped description is
		// usable, a missing name is not.
		for (const a of many) assert.ok(small.includes(a.name), `${a.name} missing`);
	});

	it("keeps full descriptions when the window can afford them", () => {
		assert.ok(!describeAgents(many, LARGE).includes("..."));
	});

	it("returns nothing for an empty roster", () => {
		assert.equal(describeAgents([], SMALL), "");
	});
});

describe("task tool", () => {
	const tool = (dispatch: Parameters<typeof createTaskTool>[0]["dispatch"]) =>
		createTaskTool({ agents: [GENERAL_PURPOSE], dispatch, tier: LARGE }) as unknown as {
			execute: (id: string, params: unknown) => Promise<{ content: Array<{ text: string }> }>;
		};

	it("dispatches to the named agent and returns its report", async () => {
		const seen: string[] = [];
		const out = await tool(async (req) => {
			seen.push(req.agent.name);
			return { text: "the answer" };
		}).execute("1", { subagent_type: "general-purpose", prompt: "find X" });
		assert.deepEqual(seen, ["general-purpose"]);
		assert.equal(out.content[0].text, "the answer");
	});

	it("names what does exist when asked for an unknown agent", async () => {
		// Turns a dead turn into a usable one.
		const out = await tool(async () => ({ text: "" })).execute("1", { subagent_type: "nope", prompt: "x" });
		assert.ok(out.content[0].text.includes("general-purpose"));
	});

	it("reports a subagent failure as a tool result, not a thrown session error", async () => {
		const out = await tool(async () => {
			throw new Error("model unreachable");
		}).execute("1", { subagent_type: "general-purpose", prompt: "x" });
		assert.ok(out.content[0].text.includes("model unreachable"));
	});

	it("does not silently return empty when the subagent says nothing", async () => {
		const out = await tool(async () => ({ text: "" })).execute("1", {
			subagent_type: "general-purpose",
			prompt: "x",
		});
		assert.ok(out.content[0].text.length > 0);
	});
});
