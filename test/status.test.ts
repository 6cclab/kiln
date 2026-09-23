import { strict as assert } from "node:assert";
import { describe, it } from "node:test";
import { renderStatus, meter, compact, elapsed } from "../src/tui/status.ts";

/**
 * The status line is a dashboard, not a label.
 *
 * What it replaced listed cwd, model, tier and budget — all true, none of which
 * changed during a session. Every segment here either moves as you work or says
 * something you cannot otherwise see.
 */

const base = { modelLabel: "ollama/qwen3.8", contextWindow: 49_152, mode: "auto", startedAt: 0, now: 3_600_000 };
const plain = (state: Parameters<typeof renderStatus>[0]) => renderStatus(state).join("\n");

describe("compact", () => {
	it("uses the units the meter sits next to", () => {
		assert.equal(compact(450_000), "450k");
		assert.equal(compact(1_000_000), "1.0m");
		assert.equal(compact(49_152), "49k");
		assert.equal(compact(1_500), "1.5k");
		assert.equal(compact(900), "900");
	});
});

describe("meter", () => {
	it("fills proportionally", () => {
		assert.equal(meter(0, 4), "░░░░");
		assert.equal(meter(1, 4), "████");
		assert.equal(meter(0.5, 4), "██░░");
	});

	it("rounds down, so a full bar means genuinely full", () => {
		// 99% must not look identical to done.
		assert.notEqual(meter(0.99, 4), meter(1, 4));
	});

	it("clamps rather than overflowing its width", () => {
		assert.equal(meter(5, 4).length, 4);
		assert.equal(meter(-1, 4), "░░░░");
	});
});

describe("elapsed", () => {
	it("stays readable across the ranges a session actually spans", () => {
		assert.equal(elapsed(30_000), "30s");
		assert.equal(elapsed(5 * 60_000), "5m");
		assert.equal(elapsed(90 * 60_000), "1h 30m");
		assert.equal(elapsed(3 * 3_600_000), "3h");
	});
});

describe("renderStatus", () => {
	it("is two rows: status, then mode with the key that changes it", () => {
		const rows = renderStatus(base);
		assert.equal(rows.length, 2);
		assert.ok(rows[1].includes("auto mode"));
		assert.ok(rows[1].includes("shift+tab"), "the key that cycles it is not shown");
	});

	it("shows context as used, total and percent", () => {
		// The number that decides whether you can keep going.
		const out = plain({ ...base, contextUsed: 22_100 });
		assert.ok(out.includes("22k"), out);
		assert.ok(out.includes("49k"), out);
		assert.ok(out.includes("45%"), out);
	});

	it("marks a dirty tree distinctly from a clean one", () => {
		// The thing you forget and rediscover during a rebase.
		const clean = plain({ ...base, git: { branch: "main", dirty: false } });
		const dirty = plain({ ...base, git: { branch: "main", dirty: true } });
		assert.notEqual(clean, dirty);
		assert.ok(clean.includes("main") && dirty.includes("main"));
	});

	it("omits cost entirely when the model is free", () => {
		// A permanent $0.00 is a segment that never earns its width, and the
		// whole point of running locally is that there is nothing to report.
		assert.ok(!plain(base).includes("$"));
		assert.ok(!plain({ ...base, cost: 0 }).includes("$"));
		assert.ok(plain({ ...base, cost: 170.32 }).includes("$170.32"));
	});

	it("omits git when there is no repository", () => {
		// A dashboard of blanks is worse than a short one.
		assert.ok(!plain(base).includes("⎇"));
	});

	it("shows a thinking indicator only while reasoning", () => {
		assert.ok(!plain(base).includes("thinking"));
		assert.ok(plain({ ...base, thinking: true }).includes("thinking"));
	});

	it("reports session age and wall-clock time", () => {
		assert.ok(plain(base).includes("1h"));
	});

	it("names the mode in every mode, including the dangerous one", () => {
		for (const mode of ["manual", "auto", "acceptEdits", "plan", "bypassPermissions"]) {
			assert.ok(renderStatus({ ...base, mode })[1].includes(mode), `${mode} not shown`);
		}
	});
});
