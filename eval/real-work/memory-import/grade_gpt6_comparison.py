#!/usr/bin/env python3
"""Offline anonymous grading for the controlled GPT-6-Sol Kiln/Codex pair."""
import json
from pathlib import Path
import grade_comparison as original
import grade_fidelity_comparison as corrected


def require_finished(cohort, include_stopped):
    expected = {(1, arm) for arm in ("codex", "kiln")}
    manifest = json.loads((cohort / "manifest.json").read_text())
    if {tuple(pair) for pair in manifest["schedule"]} != expected or len(manifest["schedule"]) != 2:
        raise RuntimeError("unexpected cohort schedule")
    stopped = cohort / "stopped.json"
    if stopped.exists() and not include_stopped:
        raise RuntimeError("inspect stopped cohort before grading")
    rows_path = cohort / "rows.jsonl"
    rows = [json.loads(line) for line in rows_path.read_text().splitlines()] if rows_path.exists() else []
    completed = {(row["repeat"], row["arm"]) for row in rows}
    if len(completed) != len(rows) or not completed <= expected:
        raise RuntimeError("duplicate or unexpected rows")
    entries = []
    for n, arm in manifest["schedule"]:
        run = cohort / "runs" / str(n) / arm
        attempt = json.loads((run / "attempt.json").read_text())
        status = attempt["status"]
        if status not in ("completed", "exited", "stopped"):
            raise RuntimeError("attempt is not terminal")
        if status != "completed" and not include_stopped:
            raise RuntimeError("attempt incomplete")
        if status == "completed" and (attempt["exit_code"] != 0 or (n, arm) not in completed):
            raise RuntimeError("completed attempt lacks successful row")
        patch = run / "changes.patch"
        if patch.stat().st_size != attempt["patch_bytes"] or original.digest(patch) != attempt["patch_sha256"]:
            raise RuntimeError("frozen patch changed")
        entries.append(dict(repeat=n, arm=arm, completion_status=status,
            exit_code=attempt.get("exit_code"), had_success_row=(n, arm) in completed))
    return entries


if __name__ == "__main__":
    original.require_finished = require_finished
    original.objective_command = corrected.command
    original.grading_env = corrected.environment
    raise SystemExit(original.main())
