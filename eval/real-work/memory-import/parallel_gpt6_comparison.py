#!/usr/bin/env python3
"""User-requested scheduling amendment for an already running GPT-6 cohort.

Let Codex 1 finish, overlap Kiln 2 with the original runner's Kiln 1, then
reconcile the original runner's expected duplicate-attempt refusal and run Codex 2.
Each Kiln gets half the remaining allowance. Kiln 2 uses its native request cap;
Kiln 1 (already started) gets an external conservative stop monitor.
"""
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import signal
import subprocess
import time
import run_gpt6_comparison as runner


def active_pid(root, repeat):
    marker = str(root / "runs" / str(repeat) / "kiln/settings.json")
    binary = str(root / "snapshots/kiln")
    for line in subprocess.check_output(["ps", "-axo", "pid=,command="], text=True).splitlines():
        fields = line.strip().split(None, 1)
        if len(fields) == 2 and fields[1].startswith(binary + " ") and marker in fields[1]:
            return int(fields[0])
    return None


def stop_kiln(root, repeat):
    pid = active_pid(root, repeat)
    if pid:
        os.killpg(pid, signal.SIGTERM)


def live_kiln_cost(run):
    total = 0.
    for path in (run / "sessions").rglob("*.jsonl"):
        lines = path.read_text().splitlines()
        if not lines:
            continue
        try:
            header = json.loads(lines[0])
        except ValueError:
            continue
        if Path(header.get("cwd", "/")).resolve() != (run / "project").resolve():
            continue
        for line in lines[1:]:
            try:
                rows = json.loads(line)
            except ValueError:
                continue
            for row in rows if isinstance(rows, list) else [rows]:
                if row.get("kind") == "usage":
                    total += row.get("usage", {}).get("cost", {}).get("total", 0)
    return total


def main():
    root = Path("/tmp/kgs-cohort").resolve()
    manifest = json.loads((root / "manifest.json").read_text())
    runner.write(root / "parallel-amendment.json", {
        "reason": "User requested parallel execution after Codex attempt 1 started.",
        "schedule": "Codex 1; Kiln 1 and 2 concurrently; Codex 2.",
        "timing_caveat": "Concurrent Kiln wall times include shared-machine contention.",
        "budget": "Split remaining allowance equally across overlapping Kiln attempts.",
        "native_kiln2_limit": True,
        "kiln1_monitor": "Stop if committed cost plus one full request reservation exceeds its allocation.",
        "watchdog_limit": "External monitor is approximate, not an invoice-level spending guarantee.",
        "coordinator_sha256": runner.common.sha256(__file__),
    })
    first = root / "runs/1/codex/summary.json"
    started = root / "runs/1/kiln/attempt.json"
    while not (first.exists() and started.exists()):
        if (root / "stopped.json").exists():
            raise RuntimeError("original runner stopped before parallel phase")
        time.sleep(.5)
    first_row = json.loads(first.read_text())
    spent = runner.PRIOR + first_row["reported_cost_usd"]
    allocation = int((runner.TOTAL - spent) / 2 * 1e6) / 1e6
    if allocation < runner.REQUEST_RESERVE:
        raise RuntimeError("remaining allowance cannot fund two concurrent Kiln reservations")
    amendment = json.loads((root / "parallel-amendment.json").read_text())
    amendment["kiln_allocation_usd"] = allocation
    runner.write(root / "parallel-amendment.json", amendment)
    runner.MAX_RUN = allocation
    print(f"Starting Kiln 2 alongside Kiln 1, allocation USD {allocation:.6f} each.", flush=True)
    with ThreadPoolExecutor(max_workers=1) as pool:
        future = pool.submit(runner.run_one, root, manifest, 2, "kiln", spent)
        try:
            while not future.done() or not (root / "runs/1/kiln/summary.json").exists():
                if live_kiln_cost(root / "runs/1/kiln") + runner.REQUEST_RESERVE > allocation:
                    stop_kiln(root, 1)
                    stop_kiln(root, 2)
                    raise RuntimeError("parallel Kiln 1 allocation exhausted; audit before further calls")
                stopped = root / "stopped.json"
                if stopped.exists():
                    reason = json.loads(stopped.read_text())["reason"]
                    if reason != "attempt exists; do not repeat paid calls":
                        stop_kiln(root, 2)
                        raise RuntimeError("original runner stopped for an unexpected reason")
                if future.done() and future.exception():
                    stop_kiln(root, 1)
                    raise future.exception()
                time.sleep(.5)
            row = future.result()
        except BaseException:
            stop_kiln(root, 1)
            stop_kiln(root, 2)
            raise
    # The original runner refuses its now-existing Kiln 2 attempt before any call.
    stopped = root / "stopped.json"
    while not stopped.exists():
        time.sleep(.1)
    marker = json.loads(stopped.read_text())
    if (marker["repeat"], marker["arm"], marker["reason"]) != (
        2, "kiln", "attempt exists; do not repeat paid calls"
    ):
        raise RuntimeError("unexpected stopped marker; no further calls")
    rows_path = root / "rows.jsonl"
    rows = [json.loads(line) for line in rows_path.read_text().splitlines()]
    if {(r["repeat"], r["arm"]) for r in rows} != {(1, "codex"), (1, "kiln")}:
        raise RuntimeError("ledger mismatch")
    with rows_path.open("a") as stream:
        stream.write(json.dumps(row) + chr(10))
    stopped.rename(root / "sequential-scheduler-refusal.json")
    runner.MAX_RUN = 14.
    print("Both Kiln attempts complete; ledger reconciled without duplicate model calls.", flush=True)
    runner.execute(root)


if __name__ == "__main__":
    main()
