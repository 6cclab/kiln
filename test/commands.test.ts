import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { CommandRegistry, staticSource } from "../src/commands/registry.ts";
import { applyArguments, splitFrontmatter } from "../src/claude/commands.ts";
import { classifyInput } from "../src/tui/input-modes.ts";

const ok = (name: string, out: string) => ({ name, description: name, run: () => ({ output: out }) });

describe("CommandRegistry", () => {
	it("resolves built-ins, plugins and project commands in one namespace", async () => {
		const reg = new CommandRegistry();
		reg.addSource(staticSource("builtin", [ok("compact", "b")]));
		reg.addSource(staticSource("plugin", [{ ...ok("a11y", "p"), namespace: "chrome" }]));
		reg.addSource(staticSource("project", [ok("track-work", "j")]));

		const names = (await reg.list()).map((c) => (c.namespace ? `${c.namespace}:${c.name}` : c.name));
		assert.deepEqual(names.sort(), ["chrome:a11y", "compact", "track-work"]);
	});

	it("lets a later source shadow an earlier one, matching settings precedence", async () => {
		const reg = new CommandRegistry();
		reg.addSource(staticSource("builtin", [ok("clear", "builtin")]));
		reg.addSource(staticSource("project", [ok("clear", "project")]));
		assert.deepEqual(await reg.execute("/clear"), { output: "project" });
	});

	it("fails loudly on an unknown command instead of sending it to the model", async () => {
		// A typo'd "/compcat fix this" forwarded as a prompt burns a full turn on a
		// local model before failing confusingly.
		const reg = new CommandRegistry();
		const result = await reg.execute("/nope do things");
		assert.ok(result?.output?.includes("Unknown command"));
	});

	it("passes non-commands through so they reach the model", async () => {
		const reg = new CommandRegistry();
		assert.equal(await reg.execute("just a normal prompt"), undefined);
	});

	it("collapses repeated leading slashes", async () => {
		// A completion bug once produced "//model", which fell through as a prompt.
		const reg = new CommandRegistry();
		reg.addSource(staticSource("builtin", [ok("model", "m")]));
		assert.deepEqual(await reg.execute("//model"), { output: "m" });
		assert.deepEqual(await reg.execute("///model anthropic/claude"), { output: "m" });
	});

	it("survives a source that throws", async () => {
		// One malformed file in .claude/commands must not empty the palette.
		const reg = new CommandRegistry();
		reg.addSource({
			origin: "project",
			load: () => {
				throw new Error("bad yaml");
			},
		});
		reg.addSource(staticSource("builtin", [ok("help", "h")]));
		assert.equal((await reg.list()).length, 1);
	});
});

describe("splitFrontmatter", () => {
	it("separates frontmatter from body", () => {
		const { data, body } = splitFrontmatter("---\ndescription: Track work\n---\nDo the thing.\n");
		assert.equal(data.description, "Track work");
		assert.equal(body.trim(), "Do the thing.");
	});

	it("treats a file without frontmatter as all body", () => {
		const { data, body } = splitFrontmatter("Just a prompt.");
		assert.deepEqual(data, {});
		assert.equal(body, "Just a prompt.");
	});

	it("keeps the body usable when the YAML is malformed", () => {
		// Losing the description is survivable; losing the command is not.
		const { body } = splitFrontmatter("---\n: : bad\n---\nStill works.\n");
		assert.equal(body.trim(), "Still works.");
	});
});

describe("applyArguments", () => {
	it("substitutes $ARGUMENTS", () => {
		assert.equal(applyArguments("Review $ARGUMENTS", "src/a.ts"), "Review src/a.ts");
	});

	it("substitutes positional $1 $2", () => {
		assert.equal(applyArguments("Compare $1 to $2", "main dev"), "Compare main to dev");
	});

	it("appends arguments when the template uses no placeholder", () => {
		// Silently dropping what the user typed is the worse failure.
		assert.equal(applyArguments("Summarize", "the repo"), "Summarize\n\nthe repo");
	});

	it("leaves a placeholder template alone when no arguments are given", () => {
		assert.equal(applyArguments("Review $ARGUMENTS", ""), "Review ");
	});
});

describe("classifyInput", () => {
	it("recognises ! and # as modes, not prompts", () => {
		assert.deepEqual(classifyInput("!ls -la"), { mode: "bang", body: "ls -la" });
		assert.deepEqual(classifyInput("#remember this"), { mode: "memory", body: "remember this" });
	});

	it("leaves /, @ and plain text to their own handlers", () => {
		for (const line of ["/help", "@src/cli.ts", "normal text"]) {
			assert.equal(classifyInput(line), undefined);
		}
	});

	it("ignores a bare prefix with no body", () => {
		assert.equal(classifyInput("!"), undefined);
		assert.equal(classifyInput("#"), undefined);
	});
});
