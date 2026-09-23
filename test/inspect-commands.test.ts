import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { inspectCommands, type InspectDeps } from "../src/commands/inspect-commands.ts";
import { PermissionGate } from "../src/claude/permission.ts";
import { tierForWindow } from "../src/budget/tier.ts";

const base = (over: Partial<InspectDeps> = {}): InspectDeps => ({
	mcpStatuses: () => [],
	gate: new PermissionGate({ permissions: { allow: [], deny: [], ask: [] }, mode: "manual", roots: ["/w"] }),
	hooks: {},
	agents: [],
	tier: tierForWindow(32_768),
	cwd: "/w",
	modelLabel: "ollama/qwen3.8:latest",
	activeTools: async () => ["bash", "read"],
	settingsLoadedFrom: ["user"],
	...over,
});

const run = async (deps: InspectDeps, name: string) => {
	const commands = await inspectCommands(deps).load();
	const command = commands.find((c) => c.name === name);
	assert.ok(command, `${name} not registered`);
	return (await command.run({ args: "" })).output ?? "";
};

describe("/mcp", () => {
	it("reports a failed server's error, which is the reason to list it", async () => {
		const out = await run(
			base({
				mcpStatuses: () => [
					{ name: "grafana", ok: true, toolCount: 81, ms: 147 },
					{ name: "proxmox", ok: false, error: "spawn tsx ENOENT", toolCount: 0, ms: 5 },
				],
			}),
			"mcp",
		);
		assert.ok(out.includes("1/2 connected"));
		assert.ok(out.includes("spawn tsx ENOENT"), "the error was not shown");
	});

	it("says where servers come from when there are none", async () => {
		assert.ok((await run(base(), "mcp")).includes(".claude.json"));
	});
});

describe("/permissions", () => {
	it("lists deny before allow, matching how the decision is made", async () => {
		const gate = new PermissionGate({
			permissions: { allow: ["Read"], deny: ["Bash(rm:*)"], ask: [] },
			mode: "manual",
			roots: ["/w"],
		});
		const out = await run(base({ gate }), "permissions");
		assert.ok(out.indexOf("deny") < out.indexOf("allow"), "allow was listed first");
	});

	it("surfaces session grants, which exist in no file", async () => {
		// A session that quietly accumulated ten grants looks identical to one
		// with none unless this is shown.
		const gate = new PermissionGate({ permissions: { allow: [], deny: [], ask: [] }, mode: "manual", roots: ["/w"] });
		gate.setPrompter(async () => ({ kind: "allow-always" }));
		await gate.check({ toolName: "write", primaryArg: "/w/a.ts", args: {} });
		const out = await run(base({ gate }), "permissions");
		assert.ok(out.includes("granted this session"));
		assert.ok(out.includes("not saved"), "did not say the grant is session-only");
	});
});

describe("/hooks", () => {
	const hooks = {
		PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command" as const, command: "/x/rtk.sh" }] }],
		Stop: [{ hooks: [{ type: "command" as const, command: "/x/stop.sh" }] }],
	};

	it("marks events that are parsed but not yet fired", async () => {
		// Claiming a hook is wired when it never runs is worse than omitting it.
		const out = await run(base({ hooks }), "hooks");
		const stopLine = out.split("\n").find((l) => l.startsWith("Stop"));
		assert.ok(stopLine?.includes("not yet fired"), `got: ${stopLine}`);
		const preLine = out.split("\n").find((l) => l.startsWith("PreToolUse"));
		assert.ok(!preLine?.includes("not yet fired"));
	});

	it("shows the matcher and timeout", async () => {
		const out = await run(
			base({
				hooks: { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: "/x.sh", timeout: 15 }] }] },
			}),
			"hooks",
		);
		assert.ok(out.includes("[Bash]"));
		assert.ok(out.includes("(15s)"));
	});
});

describe("/doctor", () => {
	it("reports no problems on a healthy setup", async () => {
		assert.ok((await run(base(), "doctor")).includes("No problems found"));
	});

	it("names every down MCP server", async () => {
		const out = await run(
			base({ mcpStatuses: () => [{ name: "sentry", ok: false, error: "needs OAuth", toolCount: 0, ms: 1 }] }),
			"doctor",
		);
		assert.ok(out.includes("sentry") && out.includes("needs OAuth"));
	});

	it("flags a session with no resident tools", async () => {
		// The model cannot act at all in this state, and nothing else says so.
		assert.ok((await run(base({ activeTools: async () => [] }), "doctor")).includes("cannot act"));
	});

	it("flags bypassPermissions, which is easy to leave on by accident", async () => {
		const gate = new PermissionGate({
			permissions: { allow: [], deny: [], ask: [] },
			mode: "bypassPermissions",
			roots: ["/w"],
		});
		assert.ok((await run(base({ gate }), "doctor")).includes("unchecked"));
	});
});
