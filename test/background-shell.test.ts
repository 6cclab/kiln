import { strict as assert } from "node:assert";
import { describe, it, before, after } from "node:test";
import { NodeExecutionEnv } from "@earendil-works/pi-agent-core/node";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { BackgroundShells, renderShellList } from "../src/agent/background-shell.ts";

/**
 * Background shells run real processes. These are not mocked: the update
 * protocol (`replace` / `append` / `slide`) is exactly the part that is easy to
 * get wrong, and a mock would encode the same misunderstanding as the code.
 */
describe("BackgroundShells", () => {
	let dir = "";
	let env: NodeExecutionEnv;

	const settle = (ms: number) => new Promise((r) => setTimeout(r, ms));

	before(async () => {
		dir = await mkdtemp(join(tmpdir(), "harness-shells-"));
		env = new NodeExecutionEnv({ cwd: dir });
	});

	after(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("returns immediately rather than waiting for the command", async () => {
		// The whole point: a dev server never exits, so starting one must not
		// occupy the turn.
		const shells = new BackgroundShells();
		const started = Date.now();
		const shell = shells.start("sleep 5", env);
		assert.ok(Date.now() - started < 500, "start blocked");
		assert.equal(shell.status, "running");
		shells.kill(shell.id);
	});

	it("reads incrementally, never repeating a line", async () => {
		// Returning the whole buffer on every poll would put the same lines into
		// context each time - a way to end a 32k conversation by checking on a
		// build.
		const shells = new BackgroundShells();
		const shell = shells.start("for i in 1 2 3 4; do echo tick-$i; sleep 0.25; done", env);

		await settle(600);
		const first = shells.read(shell.id)!.lines;
		assert.ok(first.length > 0, "no output on the first read");

		await settle(700);
		const second = shells.read(shell.id)!.lines;
		for (const line of second) {
			assert.ok(!first.includes(line), `"${line}" was returned twice`);
		}

		const third = shells.read(shell.id)!.lines;
		assert.deepEqual(third, [], "a read with nothing new returned content");

		const all = [...first, ...second];
		assert.deepEqual(all, ["tick-1", "tick-2", "tick-3", "tick-4"]);
	});

	it("records the exit code once the command finishes", async () => {
		const shells = new BackgroundShells();
		const shell = shells.start("exit 3", env);
		await settle(600);
		const after = shells.get(shell.id)!;
		assert.equal(after.status, "exited");
		assert.equal(after.exitCode, 3);
	});

	it("kills a process that would otherwise run forever", async () => {
		const shells = new BackgroundShells();
		const shell = shells.start("while true; do echo alive; sleep 0.2; done", env);
		await settle(500);
		assert.equal(shells.get(shell.id)!.status, "running");

		shells.kill(shell.id);
		assert.equal(shells.get(shell.id)!.status, "killed");

		shells.read(shell.id);
		await settle(600);
		assert.deepEqual(shells.read(shell.id)!.lines, [], "output kept arriving after the kill");
	});

	it("leaves an already-exited shell alone", async () => {
		const shells = new BackgroundShells();
		const shell = shells.start("echo done", env);
		await settle(500);
		assert.equal(shells.kill(shell.id)!.status, "exited", "a finished shell was marked killed");
	});

	it("kills everything still running on exit", async () => {
		// A dev server outliving the session is a port held by a process the
		// user has no handle on.
		const shells = new BackgroundShells();
		const a = shells.start("sleep 10", env);
		const b = shells.start("sleep 10", env);
		await settle(300);
		shells.killAll();
		assert.equal(shells.get(a.id)!.status, "killed");
		assert.equal(shells.get(b.id)!.status, "killed");
	});

	it("reports an unknown id rather than throwing", () => {
		const shells = new BackgroundShells();
		assert.equal(shells.read("nope"), undefined);
		assert.equal(shells.kill("nope"), undefined);
	});

	it("renders an empty list plainly", () => {
		assert.equal(renderShellList([]), "No background shells.");
	});

	it("lists running and finished shells with their commands", async () => {
		const shells = new BackgroundShells();
		shells.start("echo one", env);
		await settle(400);
		const out = renderShellList(shells.list());
		assert.ok(out.includes("echo one"));
		assert.ok(out.includes("bash_1"));
	});
});
