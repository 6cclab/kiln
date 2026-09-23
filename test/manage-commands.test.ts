import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { ModalView, type ModalSpec } from "../src/tui/modal.ts";
import { manageCommands, type ManageDeps } from "../src/commands/manage-commands.ts";
import { PermissionGate } from "../src/claude/permission.ts";

/**
 * The manage panels.
 *
 * `/permissions`, `/mcp`, `/agents` and `/config` each printed a listing and
 * stopped — which answers "what is configured" and leaves the other half,
 * changing it, to a text editor and a restart. These open something you can act
 * in.
 */

const settle = () => new Promise((r) => setTimeout(r, 30));

function harness(over: Partial<ManageDeps> = {}) {
	const gate = new PermissionGate({
		permissions: { allow: ["Read", "Bash(git *)"], deny: [], ask: [] },
		mode: "manual",
		roots: ["/w"],
	});
	const disk: string[] = [];
	const deps: ManageDeps = {
		gate,
		hub: { getStatuses: () => [], getTools: () => [] } as never,
		hooks: {},
		agents: [],
		settings: { loadedFrom: [], permissions: gate.getPermissions() } as never,
		cwd: "/w",
		modelLabel: "ollama/qwen3.8",
		saveRule: async (list, rule) => {
			gate.addRule(list, rule);
			disk.push(`+${list}:${rule}`);
		},
		removeRule: async (list, rule) => {
			gate.removeRule(list, rule);
			disk.push(`-${list}:${rule}`);
		},
		...over,
	};
	return { gate, disk, deps };
}

async function openPanel(deps: ManageDeps, name: string) {
	const command = (await manageCommands(deps).load()).find((c) => c.name === name);
	assert.ok(command, `${name} not registered`);
	const result = await command.run({ args: "" });
	assert.ok(result.modal, `${name} returned no panel`);
	const spec = result.modal as ModalSpec;
	const items = await spec.items();
	let closed = false;
	const view = new ModalView(spec, items, () => {
		closed = true;
	});
	return { view, result, isClosed: () => closed };
}

describe("panels open at all", () => {
	for (const name of ["permissions", "mcp", "agents", "config"]) {
		it(`/${name} returns a panel and a text fallback`, async () => {
			const { deps } = harness();
			const command = (await manageCommands(deps).load()).find((c) => c.name === name);
			const result = await command!.run({ args: "" });
			assert.ok(result.modal, "no panel");
			// Both: print mode has no TUI, and `harness -p "/mcp"` is a reasonable
			// thing to put in a script.
			assert.ok(result.output, "no text fallback for print mode");
		});
	}
});

describe("/permissions panel", () => {
	it("lists deny before allow, matching how a call is judged", async () => {
		const { deps, gate } = harness();
		gate.addRule("deny", "Bash(rm:*)");
		const { view } = await openPanel(deps, "permissions");
		const text = view.render(70).join("\n");
		assert.ok(text.indexOf("Bash(rm:*)") < text.indexOf("Read"), "allow was listed first");
	});

	it("cycles the mode, and the gate actually changes", async () => {
		const { deps, gate } = harness();
		const { view } = await openPanel(deps, "permissions");
		assert.equal(gate.mode, "manual");
		view.handleInput("m");
		await settle();
		assert.notEqual(gate.mode, "manual", "mode did not change");
	});

	it("promotes a rule to deny in memory AND on disk", async () => {
		// In memory so the next tool call obeys it; on disk so it survives a
		// restart. Either alone is a rule that half works.
		const { deps, gate, disk } = harness();
		const { view } = await openPanel(deps, "permissions");
		view.handleInput("p");
		await settle();
		assert.ok(gate.getPermissions().deny.length > 0, "not denied in memory");
		assert.ok(disk.some((d) => d.startsWith("+deny:")), "not written to disk");
	});

	it("reports what it did rather than changing silently", async () => {
		const { deps } = harness();
		const { view } = await openPanel(deps, "permissions");
		view.handleInput("p");
		await settle();
		assert.ok(view.render(70).join("\n").includes("denied"), "no confirmation shown");
	});

	it("says session grants cannot be deleted, instead of appearing to try", async () => {
		const { deps, gate } = harness();
		gate.setPrompter(async () => ({ kind: "allow-always" }));
		await gate.check({ toolName: "write", primaryArg: "/w/a.ts", args: {} });
		const { view } = await openPanel(deps, "permissions");
		const text = view.render(70).join("\n");
		assert.ok(text.includes("session only"), "session grants not distinguished from saved rules");
	});
});

describe("/mcp panel", () => {
	const statuses = [
		{ name: "grafana", ok: true, toolCount: 81, ms: 147 },
		{ name: "proxmox", ok: false, error: "spawn tsx ENOENT", toolCount: 0, ms: 5 },
	];

	it("shows the failure reason, which is the point of listing a dead server", async () => {
		const { deps } = harness({ hub: { getStatuses: () => statuses, getTools: () => [] } as never });
		const { view } = await openPanel(deps, "mcp");
		assert.ok(view.render(80).join("\n").includes("spawn tsx ENOENT"));
	});

	it("puts working servers first", async () => {
		const { deps } = harness({ hub: { getStatuses: () => statuses, getTools: () => [] } as never });
		const { view } = await openPanel(deps, "mcp");
		const text = view.render(80).join("\n");
		assert.ok(text.indexOf("grafana") < text.indexOf("proxmox"));
	});
});

describe("modal behaviour", () => {
	it("closes on Esc", async () => {
		const { deps } = harness();
		const { view, isClosed } = await openPanel(deps, "config");
		view.handleInput("\x1b");
		assert.ok(isClosed(), "Esc did not close the panel");
	});

	it("says what every key does, since it has taken the keyboard", async () => {
		// A modal that swallows input without a footer leaves Esc as the only
		// discoverable action.
		const { deps } = harness();
		const { view } = await openPanel(deps, "permissions");
		const footer = view.render(70).at(-1) ?? "";
		assert.ok(footer.includes("esc"), footer);
		assert.ok(footer.includes("cycle mode"), footer);
	});

	it("never renders wider than the terminal", async () => {
		const { deps } = harness({
			agents: [
				{
					name: "x".repeat(200),
					description: "y".repeat(300),
					prompt: "z",
					source: "project",
					path: "/p",
				},
			] as never,
		});
		const { view } = await openPanel(deps, "agents");
		for (const width of [40, 60, 100]) {
			for (const line of view.render(width)) {
				assert.ok(line.length <= width + 16, `line of ${line.length} at width ${width}`);
			}
		}
	});
});

describe("Enter selects a row", () => {
	/**
	 * `SelectList` fires `onSelect` on Enter natively, and nothing was wired to
	 * it. Every panel was a list you could move a cursor through but not choose
	 * from — which, from the keyboard, is a list that does nothing.
	 */
	it("fires onSelect and can close the panel", async () => {
		const chosen: string[] = [];
		let closed = false;
		const view = new ModalView(
			{
				title: "T",
				items: () => [
					{ value: "a", label: "A" },
					{ value: "b", label: "B" },
				],
				onSelect: (item) => {
					chosen.push(item.value);
					return { close: true as const };
				},
			},
			[
				{ value: "a", label: "A" },
				{ value: "b", label: "B" },
			],
			() => {
				closed = true;
			},
		);

		view.handleInput("\x1b[B");
		view.handleInput("\r");
		await new Promise((r) => setTimeout(r, 30));

		assert.deepEqual(chosen, ["b"], "Enter did not select the row under the cursor");
		assert.ok(closed, "returning { close: true } did not close the panel");
	});

	it("advertises Enter and the arrow keys in the footer", async () => {
		// A panel that responds to keys it never names is a panel you have to
		// guess at.
		const view = new ModalView(
			{ title: "T", items: () => [{ value: "a", label: "A" }], selectLabel: "use it", onSelect: () => undefined },
			[{ value: "a", label: "A" }],
			() => {},
		);
		const footer = view.render(70).at(-1) ?? "";
		assert.ok(footer.includes("enter"), footer);
		assert.ok(footer.includes("use it"), footer);
		assert.ok(footer.includes("move"), footer);
	});

	it("omits the Enter hint when a row does nothing", async () => {
		const view = new ModalView({ title: "T", items: () => [{ value: "a", label: "A" }] }, [{ value: "a", label: "A" }], () => {});
		assert.ok(!(view.render(70).at(-1) ?? "").includes("enter"));
	});
})
