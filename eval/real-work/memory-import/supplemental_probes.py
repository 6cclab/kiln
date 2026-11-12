#!/usr/bin/env python3
"""Post-submission reviewer probes, separate from the frozen acceptance suite.

Added after static review of the first two anonymous patches. Apply identically
to all candidates; these are not predeclared acceptance cases or timed model runs.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sqlite3
import tempfile


def record(number, **extra):
    value = dict(id=number, scope="/old/project/.git", key=f"key-{number}",
                 kind="observation", text="Imported fixture", evidence="Reviewer fixture",
                 created=1, expires=0, supersedes=0)
    value.update(extra)
    return value


def probe(binary, name):
    with tempfile.TemporaryDirectory(prefix="kiln-review-probe-") as temporary:
        root = Path(temporary)
        home, project = root / "home", root / "project"
        home.mkdir()
        project.mkdir()
        env = {k: os.environ[k] for k in ("PATH", "LANG", "TERM") if k in os.environ}
        env.update(HOME=str(home), GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
        subprocess.run(["git", "init", "-q", str(project)], env=env, check=True, capture_output=True)
        if name == "existing_memory_help_preserved":
            result = subprocess.run([binary, "memory", "--help"], cwd=project,
                                    env=env, text=True, capture_output=True, timeout=10)
            passed = result.returncode == 0 and "usage:" in result.stdout
            return dict(probe=name, status="passed" if passed else "failed",
                        exit_code=result.returncode, diagnostic=result.stderr[:500])
        flags = []
        expected_counts = dict(inserted=1000, replaced=0, skipped=0, dry_run=False)
        if name == "fifo_source_rejected_without_blocking":
            if not hasattr(os, "mkfifo"):
                return dict(probe=name, status="unsupported", reason="platform has no mkfifo")
            os.mkfifo(project / "source.pipe", 0o600)
            data = [record(1, sources=[dict(root="/old/project", path="/old/project/source.pipe",
                                           sha256=hashlib.sha256(b"").hexdigest())])]
            expected_success, seconds = False, 3
        elif name == "dry_run_does_not_attempt_memory_writes":
            subprocess.run([binary, "memory", "remember", "anchor", "Existing fixture"],
                           cwd=project, env=env, check=True, capture_output=True, timeout=10)
            with sqlite3.connect(home / ".kiln" / "memory.db") as db:
                db.execute("CREATE TRIGGER reject_review_write BEFORE INSERT ON memories "
                           "BEGIN SELECT RAISE(ABORT, 'review probe observed INSERT'); END")
            data = [record(1)]
            flags = ["--dry-run"]
            expected_counts = dict(inserted=1, replaced=0, skipped=0, dry_run=True)
            expected_success, seconds = True, 10
        else:
            data = [record(i) for i in range(1, 1001)]
            expected_success, seconds = True, 30
        source = root / "input.json"
        source.write_text(json.dumps(data))
        try:
            result = subprocess.run([binary, "memory", "import", str(source), *flags], cwd=project,
                                    env=env, text=True, capture_output=True, timeout=seconds)
        except subprocess.TimeoutExpired:
            return dict(probe=name, status="failed", reason=f"did not return within {seconds}s; process terminated")
        if expected_success:
            try:
                summary = json.loads(result.stdout)
                passed = result.returncode == 0 and all(summary.get(k) == v for k, v in
                    expected_counts.items())
            except (ValueError, AttributeError):
                passed = False
        else:
            passed = result.returncode != 0 and bool(result.stderr.strip())
        return dict(probe=name, status="passed" if passed else "failed", exit_code=result.returncode,
                    diagnostic=result.stderr[:500])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve(strict=True))
    outcomes = [probe(binary, name) for name in
                ("fifo_source_rejected_without_blocking", "exactly_1000_records_accepted",
                 "dry_run_does_not_attempt_memory_writes", "existing_memory_help_preserved")]
    for outcome in outcomes:
        print(json.dumps(outcome, sort_keys=True))
    return int(any(outcome["status"] == "failed" for outcome in outcomes))


if __name__ == "__main__":
    raise SystemExit(main())
