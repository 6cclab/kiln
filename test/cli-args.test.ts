import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { parseArgs, splitToolList } from "../src/cli-args.ts";

/**
 * Flag names and semantics come from `claude --help`. The point of matching
 * them is muscle memory: `claude -c` and `harness -c` should do the same thing.
 */
describe("parseArgs", () => {
	it("accepts both the space form and the equals form", () => {
		// The old indexOf-based lookup handled only the space form and silently
		// ignored the other, which is the form scripts tend to use.
		assert.equal(parseArgs(["--model", "ollama/qwen3.8"]).model, "ollama/qwen3.8");
		assert.equal(parseArgs(["--model=ollama/qwen3.8"]).model, "ollama/qwen3.8");
	});

	it("reads the short aliases", () => {
		assert.equal(parseArgs(["-c"]).continueLatest, true);
		assert.equal(parseArgs(["-p", "hello"]).print, true);
		assert.equal(parseArgs(["-v"]).version, true);
		assert.equal(parseArgs(["-h"]).help, true);
		assert.equal(parseArgs(["-n", "my-session"]).name, "my-session");
	});

	it("treats --resume with no id as resuming the latest", () => {
		// A following flag must not be swallowed as the id.
		assert.equal(parseArgs(["--resume"]).resume, true);
		assert.equal(parseArgs(["--resume", "--verbose"]).resume, true);
		assert.equal(parseArgs(["--resume", "--verbose"]).verbose, true);
		assert.equal(parseArgs(["--resume", "abc123"]).resume, "abc123");
	});

	it("collects the print prompt from the positional text", () => {
		const args = parseArgs(["-p", "what", "does", "this", "do"]);
		assert.equal(args.printPrompt, "what does this do");
	});

	it("recognises subcommands without treating them as a prompt", () => {
		const args = parseArgs(["login", "anthropic"]);
		assert.equal(args.command, "login");
		assert.deepEqual(args.positional, ["anthropic"]);
	});

	it("accumulates --add-dir rather than keeping only the last", () => {
		const args = parseArgs(["--add-dir", "/a", "--add-dir", "/b"]);
		assert.deepEqual(args.addDir, ["/a", "/b"]);
	});

	it("validates enumerated values instead of passing anything through", () => {
		// A typo'd effort level must not reach the model layer as a mode nobody
		// checks.
		assert.equal(parseArgs(["--effort", "high"]).effort, "high");
		assert.equal(parseArgs(["--effort", "extreme"]).effort, undefined);
		assert.equal(parseArgs(["--output-format", "json"]).outputFormat, "json");
		assert.equal(parseArgs(["--output-format", "yaml"]).outputFormat, undefined);
	});

	it("reports unknown flags rather than ignoring them", () => {
		// A mistyped flag that silently does nothing is how a script ends up not
		// doing what it says.
		assert.deepEqual(parseArgs(["--not-a-flag"]).unknown, ["--not-a-flag"]);
		assert.deepEqual(parseArgs(["--verbose"]).unknown, []);
	});

	it("parses a realistic invocation end to end", () => {
		const args = parseArgs([
			"-p",
			"fix the build",
			"--model=ollama/qwen3.8:latest",
			"--permission-mode",
			"acceptEdits",
			"--add-dir",
			"/tmp/x",
			"--allowed-tools",
			"Bash(git *) Edit",
			"--output-format=json",
			"--verbose",
		]);
		assert.equal(args.printPrompt, "fix the build");
		assert.equal(args.model, "ollama/qwen3.8:latest");
		assert.equal(args.permissionMode, "acceptEdits");
		assert.deepEqual(args.addDir, ["/tmp/x"]);
		assert.deepEqual(args.allowedTools, ["Bash(git *)", "Edit"]);
		assert.equal(args.outputFormat, "json");
		assert.equal(args.verbose, true);
	});
});

describe("splitToolList", () => {
	it("keeps a rule with spaces inside parentheses intact", () => {
		// Splitting on whitespace turns one rule into two, and `Bash(git` is a
		// rule that matches nothing.
		assert.deepEqual(splitToolList("Bash(git *) Edit"), ["Bash(git *)", "Edit"]);
	});

	it("handles several parenthesised rules", () => {
		assert.deepEqual(splitToolList("Bash(npm run *) Bash(git commit *) Read"), [
			"Bash(npm run *)",
			"Bash(git commit *)",
			"Read",
		]);
	});

	it("accepts the comma form people also use", () => {
		assert.deepEqual(splitToolList("Read,Write,Edit"), ["Read", "Write", "Edit"]);
	});

	it("does not split inside parentheses on a comma", () => {
		assert.deepEqual(splitToolList("Bash(a,b)"), ["Bash(a,b)"]);
	});

	it("returns nothing for empty input", () => {
		assert.deepEqual(splitToolList(""), []);
		assert.deepEqual(splitToolList("   "), []);
	});
});
