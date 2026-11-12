#!/usr/bin/env python3
"""Anonymize and objectively grade frozen memory-import candidate patches.

This script makes no model calls. Both commands require --runs-finished so it
cannot accidentally start while paid timed runs are active.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import random
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time


BASELINE = "40e946486120497606d0e0070180dfb9aad24f05"
HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
HIDDEN = HERE / "hidden_acceptance.py"


def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def save_json(path, value):
    with open(path, "w", encoding="utf-8") as target:
        json.dump(value, target, indent=2, sort_keys=True)
        target.write("\n")


def git_env():
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env.update({"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_LFS_SKIP_SMUDGE": "1"})
    return env


def git(project, *args):
    return subprocess.check_output(["git", *args], cwd=project, env=git_env(), stderr=subprocess.PIPE).decode().strip()


def require_finished(cohort, include_stopped):
    stopped_path = cohort / "stopped.json"
    stopped = json.loads(stopped_path.read_text()) if stopped_path.exists() else None
    if stopped and not include_stopped:
        raise RuntimeError("live cohort stopped; pass --include-stopped-attempt after root review")
    manifest = json.loads((cohort / "manifest.json").read_text())
    expected = {(repeat, arm) for repeat in (1, 2) for arm in ("claude", "kiln")}
    if {tuple(pair) for pair in manifest["schedule"]} != expected or len(manifest["schedule"]) != 4:
        raise RuntimeError("cohort schedule is not the frozen four attempts")
    rows_path = cohort / "rows.jsonl"
    if not rows_path.exists():
        raise RuntimeError("live cohort has no completed rows")
    rows = [json.loads(line) for line in rows_path.read_text().splitlines()]
    row_keys = {(r["repeat"], r["arm"]) for r in rows}
    if len(row_keys) != len(rows) or not row_keys <= expected:
        raise RuntimeError("cohort rows have duplicate or unexpected attempts")
    entries = []
    failed = []
    for repeat, arm in manifest["schedule"]:
        run_dir = cohort / "runs" / str(repeat) / arm
        attempt_path = run_dir / "attempt.json"
        patch_path = run_dir / "changes.patch"
        if not attempt_path.is_file() or not patch_path.is_file():
            raise RuntimeError(f"attempt metadata or frozen patch missing for {repeat} {arm}")
        attempt = json.loads(attempt_path.read_text())
        status = attempt.get("status")
        if status not in ("completed", "exited", "timed_out"):
            raise RuntimeError(f"attempt {repeat} {arm} is not terminal")
        if attempt.get("patch_bytes") != patch_path.stat().st_size:
            raise RuntimeError(f"attempt {repeat} {arm} patch size changed")
        has_row = (repeat, arm) in row_keys
        if status == "completed" and (attempt.get("exit_code") != 0 or not has_row):
            raise RuntimeError(f"completed attempt {repeat} {arm} lacks successful row")
        if status != "completed":
            failed.append((repeat, arm))
            if has_row:
                raise RuntimeError(f"failed attempt {repeat} {arm} has a fabricated success row")
        entries.append({"repeat": repeat, "arm": arm, "completion_status": status,
                        "exit_code": attempt.get("exit_code"), "had_success_row": has_row})
    if stopped:
        if len(failed) != 1 or failed[0] != (stopped.get("repeat"), stopped.get("arm")):
            raise RuntimeError("stopped marker does not match the sole terminal failed attempt")
    elif failed or len(rows) != 4:
        raise RuntimeError("cohort is incomplete without a stopped marker")
    return entries


def separate_paths(bundle_root, map_path):
    bundle_root = bundle_root.resolve()
    map_path = map_path.resolve()
    if bundle_root == map_path or bundle_root in map_path.parents or map_path in bundle_root.parents:
        raise RuntimeError("identity map must live outside the review bundle tree")


def copy_candidate(source_repo, baseline, patch_path, bundle):
    source = bundle / "source"
    env = git_env()
    subprocess.check_call(["git", "clone", "--no-local", "--no-checkout", str(source_repo), str(source)], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    git(source, "checkout", "--detach", baseline)
    git(source, "remote", "remove", "origin")
    if git(source, "rev-parse", "HEAD") != baseline or git(source, "status", "--porcelain=v1") or git(source, "remote", "-v"):
        raise RuntimeError("anonymous source is not an independent clean baseline")
    copied = bundle / "changes.patch"
    shutil.copyfile(patch_path, copied)
    apply_error = None
    if copied.stat().st_size:
        try:
            with open(copied, "rb") as patch:
                subprocess.run(["git", "apply", "--check", "--binary", "-"], cwd=source, env=env, stdin=patch, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            with open(copied, "rb") as patch:
                subprocess.run(["git", "apply", "--binary", "-"], cwd=source, env=env, stdin=patch, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        except subprocess.CalledProcessError as exc:
            apply_error = exc.stderr.decode(errors="replace")
    # Stage only in the anonymous clone so a later diff also includes files
    # newly created by the submitted patch.
    subprocess.run(["git", "-c", "core.hooksPath=/dev/null", "add", "-A"], cwd=source, env=env, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    return source, copied, apply_error


def prepare(args):
    cohort = Path(args.cohort).expanduser().resolve(strict=True)
    entries = require_finished(cohort, args.include_stopped_attempt)
    bundle_root = Path(args.bundle_root).expanduser().resolve()
    map_path = Path(args.identity_map).expanduser().resolve()
    separate_paths(bundle_root, map_path)
    if bundle_root.exists() or map_path.exists():
        raise RuntimeError("bundle root and identity map must not exist before preparation")
    if git(REPO, "rev-parse", BASELINE) != BASELINE:
        raise RuntimeError("frozen baseline commit unavailable")
    bundle_root.mkdir(parents=True, mode=0o700)
    map_path.parent.mkdir(parents=True, exist_ok=True)
    seed = secrets.token_hex(32)
    random.Random(seed).shuffle(entries)
    mapping = {"baseline": BASELINE, "seed": seed, "cohort": str(cohort), "candidates": {}}
    labels = []
    for number, entry in enumerate(entries, 1):
        repeat, arm = entry["repeat"], entry["arm"]
        label = f"candidate-{number:02d}"
        labels.append(label)
        patch_path = cohort / "runs" / str(repeat) / arm / "changes.patch"
        if not patch_path.is_file():
            raise RuntimeError(f"frozen patch missing for repeat {repeat} {arm}")
        patch_hash = digest(patch_path)
        bundle = bundle_root / label
        bundle.mkdir(mode=0o700)
        source, copied, apply_error = copy_candidate(REPO, BASELINE, patch_path, bundle)
        if digest(copied) != patch_hash or digest(patch_path) != patch_hash:
            raise RuntimeError("patch bytes changed while preparing anonymous candidate")
        (bundle / "baseline.txt").write_text(BASELINE + "\n")
        mapping["candidates"][label] = {
            "repeat": repeat, "arm": arm, "original_patch": str(patch_path),
            "patch_sha256": patch_hash, "bundle": str(bundle),
            "source": str(source),
            "completion_status": entry["completion_status"],
            "exit_code": entry["exit_code"],
            "had_success_row": entry["had_success_row"],
            "apply_error": apply_error,
        }
    mapping["reviewer_1_order"] = labels
    mapping["reviewer_2_order"] = list(reversed(labels))
    save_json(map_path, mapping)
    map_path.chmod(0o600)
    print(f"prepared {len(labels)} anonymous candidate bundles; identity map kept outside review tree")


def grading_env(private_dir):
    home = private_dir / "home"
    temp = private_dir / "tmp"
    home.mkdir(mode=0o700)
    temp.mkdir(mode=0o700)
    env = {k: os.environ[k] for k in ("PATH", "LANG", "LC_ALL", "TERM", "SHELL") if k in os.environ}
    env.update({
        "HOME": str(home), "TMPDIR": str(temp), "XDG_CONFIG_HOME": str(home / ".config"),
        "GOPROXY": "off", "GOFLAGS": "-mod=readonly", "GOTOOLCHAIN": "go1.26.6",
        "GOMODCACHE": subprocess.check_output(["go", "env", "GOMODCACHE"], text=True).strip(),
        "GOCACHE": subprocess.check_output(["go", "env", "GOCACHE"], text=True).strip(),
        "GOPATH": str(home / "go"), "GOBIN": str(home / "go" / "bin"),
        "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
    })
    return env


def objective_command(source, grading, name, argv, env, timeout=600):
    stdout_path = grading / f"{name}.stdout.txt"
    stderr_path = grading / f"{name}.stderr.txt"
    start = time.monotonic()
    with open(stdout_path, "wb") as stdout, open(stderr_path, "wb") as stderr:
        process = subprocess.Popen(argv, cwd=source, env=env, stdout=stdout, stderr=stderr, start_new_session=True)
        try:
            exit_code = process.wait(timeout=timeout)
            state = "completed"
        except subprocess.TimeoutExpired:
            state = "timeout"
            exit_code = None
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait()
    return {
        "name": name, "command": argv, "state": state, "exit_code": exit_code,
        "duration_ms": round((time.monotonic() - start) * 1000),
        "stdout": stdout_path.name, "stderr": stderr_path.name,
    }


def tracked_diff(source):
    return subprocess.check_output(["git", "diff", "--binary", "HEAD"], cwd=source, env=git_env())


def source_status(source):
    return subprocess.check_output(
        ["git", "status", "--porcelain=v1", "--untracked-files=all"],
        cwd=source, env=git_env(),
    ).decode()


def grade_candidate(label, entry, bundle, binary_root, hidden_hash):
    source = bundle / "source"
    patch_path = bundle / "changes.patch"
    if digest(entry["original_patch"]) != entry["patch_sha256"] or digest(patch_path) != entry["patch_sha256"]:
        raise RuntimeError("frozen patch SHA mismatch")
    before = hashlib.sha256(tracked_diff(source)).hexdigest()
    status_before = source_status(source)
    grading = bundle / "grading"
    grading.mkdir(exist_ok=False)
    (grading / "git-status-before.txt").write_text(status_before + "\n")
    if entry["apply_error"] is not None:
        (grading / "preparation.stderr.txt").write_text(entry["apply_error"])
        results = [{"name": name, "state": "unrun", "exit_code": None,
                    "reason": "frozen patch did not apply to baseline"}
                   for name in ("gofmt", "build", "vet", "staticcheck", "unit", "vet-e2e",
                                "race-memory-cli", "e2e-memory", "hidden-acceptance")]
        outcome = {"baseline": BASELINE, "candidate": label,
                   "patch_sha256": entry["patch_sha256"], "patch_bytes": patch_path.stat().st_size,
                   "patch_applied": False, "commands": results}
        save_json(grading / "outcomes.json", outcome)
        return outcome
    private_dir = binary_root / label
    private_dir.mkdir(parents=True, exist_ok=False)
    env = grading_env(private_dir)
    binary = private_dir / "kiln"
    commands = [
        ("gofmt", ["gofmt", "-l", "."]),
        ("build", ["go", "build", "-o", str(binary), "./cmd/kiln"]),
        ("vet", ["go", "vet", "./..."]),
        ("staticcheck", ["go", "run", "honnef.co/go/tools/cmd/staticcheck@2026.2.2", "./..."]),
        ("unit", ["go", "test", "./...", "-count=1"]),
        ("vet-e2e", ["go", "vet", "-tags", "e2e", "./test/e2e/..."]),
        ("race-memory-cli", ["go", "test", "-race", "./internal/memory", "./internal/cli", "-count=1"]),
        ("e2e-memory", ["go", "test", "-tags=e2e", "./test/e2e", "-run", "Memory|Preference|Import", "-count=1"]),
    ]
    results = []
    for name, argv in commands:
        outcome = objective_command(source, grading, name, argv, env)
        if name == "gofmt" and outcome["exit_code"] == 0:
            content = (grading / outcome["stdout"]).read_text()
            outcome["formatted"] = content.strip() == ""
        results.append(outcome)
        save_json(grading / "outcomes.json", {"baseline": BASELINE, "candidate": label, "commands": results})
    if binary.is_file():
        if digest(HIDDEN) != hidden_hash:
            raise RuntimeError("hidden grader changed after freeze")
        hidden = objective_command(source, grading, "hidden-acceptance", [sys.executable, str(HIDDEN), "--binary", str(binary)], env, timeout=600)
        results.append(hidden)
    else:
        results.append({"name": "hidden-acceptance", "state": "unrun", "exit_code": None, "reason": "candidate binary did not build"})
    after = hashlib.sha256(tracked_diff(source)).hexdigest()
    status_after = source_status(source)
    (grading / "git-status-after.txt").write_text(status_after + "\n")
    outcome = {
        "baseline": BASELINE, "candidate": label, "patch_sha256": entry["patch_sha256"],
        "patch_bytes": patch_path.stat().st_size,
        "patch_applied": True,
        "hidden_grader_sha256": hidden_hash,
        "tracked_diff_before_sha256": before, "tracked_diff_after_sha256": after,
        "tracked_source_unchanged_by_grading": before == after,
        "status_before_sha256": hashlib.sha256(status_before.encode()).hexdigest(),
        "status_after_sha256": hashlib.sha256(status_after.encode()).hexdigest(),
        "status_unchanged_by_grading": status_before == status_after,
        "commands": results,
    }
    save_json(grading / "outcomes.json", outcome)
    if digest(entry["original_patch"]) != entry["patch_sha256"] or digest(patch_path) != entry["patch_sha256"]:
        raise RuntimeError("frozen patch changed during grading")
    return outcome


def grade(args):
    bundle_root = Path(args.bundle_root).expanduser().resolve(strict=True)
    map_path = Path(args.identity_map).expanduser().resolve(strict=True)
    binary_root = Path(args.binary_root).expanduser().resolve()
    separate_paths(bundle_root, map_path)
    separate_paths(bundle_root, binary_root)
    if binary_root.exists():
        raise RuntimeError("private binary output root must not exist before grading")
    binary_root.mkdir(parents=True)
    mapping = json.loads(map_path.read_text())
    if mapping["baseline"] != BASELINE:
        raise RuntimeError("identity map baseline mismatch")
    hidden_hash = digest(HIDDEN)
    if hidden_hash != args.hidden_sha256:
        raise RuntimeError("hidden grader SHA differs from frozen preflight")
    for label in mapping["reviewer_1_order"]:
        entry = mapping["candidates"][label]
        outcome = grade_candidate(label, entry, bundle_root / label, binary_root, hidden_hash)
        print(f"{label}: {len(outcome['commands'])} named checks, tracked source unchanged={outcome['tracked_source_unchanged_by_grading']}", flush=True)


def repair_env(label):
    home = Path(tempfile.mkdtemp(prefix=f"kg{label[-2:]}h-", dir="/tmp"))
    temp = Path(tempfile.mkdtemp(prefix=f"kg{label[-2:]}t-", dir="/tmp"))
    module_cache = subprocess.check_output(["go", "env", "GOMODCACHE"], text=True).strip()
    build_cache = subprocess.check_output(["go", "env", "GOCACHE"], text=True).strip()
    env = {k: os.environ[k] for k in ("PATH", "LANG", "LC_ALL", "TERM", "SHELL") if k in os.environ}
    env.update({
        "HOME": str(home), "TMPDIR": str(temp), "XDG_CONFIG_HOME": str(home / ".config"),
        "GOPROXY": "file://" + str(Path(module_cache) / "cache" / "download"),
        "GOFLAGS": "-mod=readonly", "GOTOOLCHAIN": "go1.26.6",
        "GOMODCACHE": module_cache, "GOCACHE": build_cache,
        "GOPATH": str(home / "go"), "GOBIN": str(home / "go" / "bin"),
        "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
    })
    return env


def repair_infrastructure(args):
    bundle_root = Path(args.bundle_root).expanduser().resolve(strict=True)
    map_path = Path(args.identity_map).expanduser().resolve(strict=True)
    separate_paths(bundle_root, map_path)
    mapping = json.loads(map_path.read_text())
    if mapping["baseline"] != BASELINE:
        raise RuntimeError("identity map baseline mismatch")
    for label in mapping["reviewer_1_order"]:
        bundle = bundle_root / label
        source = bundle / "source"
        grading = bundle / "grading"
        entry = mapping["candidates"][label]
        if digest(entry["original_patch"]) != entry["patch_sha256"] or digest(bundle / "changes.patch") != entry["patch_sha256"]:
            raise RuntimeError("frozen patch SHA mismatch before infrastructure rerun")
        before_diff = hashlib.sha256(tracked_diff(source)).hexdigest()
        before_status = source_status(source)
        env = repair_env(label)
        version = objective_command(source, grading, "staticcheck-offline-version", ["go", "run", "honnef.co/go/tools/cmd/staticcheck@v0.8.2", "-version"], env)
        version_text = (grading / version["stdout"]).read_text().strip()
        if version["exit_code"] != 0 or version_text != "staticcheck 2026.2.2 (0.8.2)":
            raise RuntimeError(f"offline staticcheck version could not be verified for {label}")
        reruns = [version]
        reruns.append(objective_command(source, grading, "staticcheck-offline", ["go", "run", "honnef.co/go/tools/cmd/staticcheck@v0.8.2", "./..."], env))
        reruns.append(objective_command(source, grading, "unit-shorttmp", ["go", "test", "./...", "-count=1"], env))
        after_diff = hashlib.sha256(tracked_diff(source)).hexdigest()
        after_status = source_status(source)
        outcome_path = grading / "outcomes.json"
        outcome = json.loads(outcome_path.read_text())
        outcome["infrastructure_reruns"] = reruns
        outcome["infrastructure_rerun_source_unchanged"] = before_diff == after_diff and before_status == after_status
        save_json(outcome_path, outcome)
        if digest(entry["original_patch"]) != entry["patch_sha256"] or digest(bundle / "changes.patch") != entry["patch_sha256"]:
            raise RuntimeError("frozen patch SHA changed during infrastructure rerun")
        print(f"{label}: staticcheck={reruns[1]['exit_code']} unit={reruns[2]['exit_code']} source unchanged={outcome['infrastructure_rerun_source_unchanged']}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    prepare_parser = sub.add_parser("prepare")
    prepare_parser.add_argument("--cohort", required=True)
    prepare_parser.add_argument("--bundle-root", required=True)
    prepare_parser.add_argument("--identity-map", required=True)
    prepare_parser.add_argument("--runs-finished", action="store_true", required=True)
    prepare_parser.add_argument("--include-stopped-attempt", action="store_true")
    grade_parser = sub.add_parser("grade")
    grade_parser.add_argument("--bundle-root", required=True)
    grade_parser.add_argument("--identity-map", required=True)
    grade_parser.add_argument("--binary-root", required=True)
    grade_parser.add_argument("--hidden-sha256", required=True)
    grade_parser.add_argument("--runs-finished", action="store_true", required=True)
    repair_parser = sub.add_parser("repair-infrastructure")
    repair_parser.add_argument("--bundle-root", required=True)
    repair_parser.add_argument("--identity-map", required=True)
    repair_parser.add_argument("--runs-finished", action="store_true", required=True)
    args = parser.parse_args()
    if not args.runs_finished:
        parser.error("objective preparation and grading require --runs-finished")
    try:
        if args.command == "prepare":
            prepare(args)
        elif args.command == "grade":
            grade(args)
        else:
            repair_infrastructure(args)
    except Exception as exc:
        print(f"objective grading: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
