import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { decide, matchesRule, type Permissions } from "../src/claude/settings.ts";

/**
 * Permission rule matching.
 *
 * Every case below is a real format found in `~/.claude/settings.json`. Three
 * of them were bugs that shipped: case-sensitivity, the colon form, and bare
 * `mcp__` prefixes. All three failed *open* — every rule missed, so the gate
 * silently degraded into prompting for everything rather than erroring.
 */
describe("matchesRule", () => {
	it("matches a bare tool name", () => {
		assert.equal(matchesRule("Read", "read"), true);
		assert.equal(matchesRule("Read", "bash", "x"), false);
	});

	it("is case-insensitive: Claude writes Read, pi's tool is read", () => {
		assert.equal(matchesRule("Read", "read"), true);
		assert.equal(matchesRule("BASH", "bash", "ls"), true);
	});

	it("handles the colon form used in real settings: Bash(find:*)", () => {
		assert.equal(matchesRule("Bash(find:*)", "bash", "find . -name '*.go'"), true);
		assert.equal(matchesRule("Bash(find:*)", "bash", "rm -rf /"), false);
	});

	it("colon form also matches the bare command with no arguments", () => {
		assert.equal(matchesRule("Bash(ls:*)", "bash", "ls"), true);
	});

	it("handles the space glob form documented in --help", () => {
		assert.equal(matchesRule("Bash(git *)", "bash", "git status"), true);
		assert.equal(matchesRule("Bash(git *)", "bash", "npm test"), false);
	});

	it("treats a bare mcp__server rule as a prefix over that server's tools", () => {
		assert.equal(matchesRule("mcp__homelab", "mcp__homelab-kb__hk_search"), true);
		assert.equal(matchesRule("mcp__homelab", "mcp__grafana__query"), false);
	});

	it("escapes regex metacharacters rather than interpreting them", () => {
		// The "." must be literal; otherwise this would match "npm run buildX".
		assert.equal(matchesRule("Bash(npm run build.sh)", "bash", "npm run buildXsh"), false);
		assert.equal(matchesRule("Bash(npm run build.sh)", "bash", "npm run build.sh"), true);
	});
});

describe("decide", () => {
	const permissions: Permissions = { allow: ["Read", "Bash(ls:*)"], deny: ["Bash(rm:*)"], ask: [] };

	it("deny wins over allow, so a broader rule cannot re-grant it", () => {
		const permissive: Permissions = { allow: ["Bash"], deny: ["Bash(rm:*)"], ask: [] };
		assert.equal(decide(permissive, "bash", "rm -rf /", "dontAsk"), "deny");
	});

	it("allows what the rules allow", () => {
		assert.equal(decide(permissions, "read", "f.ts", "manual"), "allow");
		assert.equal(decide(permissions, "bash", "ls -la", "manual"), "allow");
	});

	it("asks for anything unmatched in manual mode", () => {
		assert.equal(decide(permissions, "write", "f.ts", "manual"), "ask");
	});

	it("acceptEdits auto-approves edits but still asks for other tools", () => {
		assert.equal(decide(permissions, "edit", "f.ts", "acceptEdits"), "allow");
		assert.equal(decide(permissions, "write", "f.ts", "acceptEdits"), "allow");
		assert.equal(decide(permissions, "bash", "curl evil.com", "acceptEdits"), "ask");
	});

	it("plan mode refuses mutations outright rather than prompting", () => {
		// Prompting would let a distracted user approve a write in read-only mode,
		// which defeats the point of plan mode.
		assert.equal(decide(permissions, "write", "f.ts", "plan"), "deny");
		assert.equal(decide(permissions, "read", "f.ts", "plan"), "allow");
	});

	it("bypassPermissions still honours explicit denials", () => {
		assert.equal(decide(permissions, "bash", "rm -rf /", "bypassPermissions"), "deny");
		assert.equal(decide(permissions, "write", "f.ts", "bypassPermissions"), "allow");
	});
});

describe("auto mode", () => {
	const none = { allow: [], deny: [], ask: [] };

	it("allows everything, including writes", () => {
		// `auto` is the mode you pick once you have decided to stop supervising
		// this session. Prompting for a write in the project you are working in
		// defeats the point of picking it.
		for (const tool of ["read", "grep", "write", "edit", "bash"]) {
			assert.equal(decide(none, tool, undefined, "auto"), "allow", `${tool} still asked`);
		}
	});

	it("is still overridden by an explicit deny", () => {
		// Blanket means "stop asking", not "ignore the rules I wrote down".
		assert.equal(decide({ ...none, deny: ["Write"] }, "write", undefined, "auto"), "deny");
		assert.equal(decide({ ...none, deny: ["Bash(rm:*)"] }, "bash", "rm -rf /", "auto"), "deny");
	});

	it("differs from manual, which is the point", () => {
		assert.notEqual(decide(none, "write", undefined, "auto"), decide(none, "write", undefined, "manual"));
	});
});
