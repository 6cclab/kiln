#!/usr/bin/env python3
"""Prepare and explicitly run the capped Opus 4.8 memory-import comparison.

Preparation makes independent baseline clones and executable snapshots without
reading credentials or invoking a model. `run --live` is the only paid path.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import time


BASELINE = "40e946486120497606d0e0070180dfb9aad24f05"
MODEL = "claude-opus-4-8"
PRIOR_REPORTED_USD = 1.1484105
TOTAL_CAP_USD = 50.0
CLAUDE_RUN_CAP_USD = 6.0
CLAUDE_HEADROOM_USD = 2.0
KILN_MAX_USD = 35.0
KILN_FULL_CONTEXT_RESERVE_USD = 20.7
TURN_CAP = 80
WALL_CAP_SECONDS = 20 * 60
SEED = "memory-import-40e9464-opus48-20261111-0"
REPO = Path(__file__).resolve().parents[3]
TASK = Path(__file__).with_name("task.md")
RUBRIC = Path(__file__).with_name("rubric.md")
HIDDEN = Path(__file__).with_name("hidden_acceptance.py")
FROZEN_HIDDEN_SHA256 = "694676000b8157be18b6ac8161d9586b0ad36e321d90f511b74c6905a3f1be6d"
REVIEWER = Path(__file__).with_name("reviewer-instructions.md")


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def write_json(path, value):
    path = Path(path)
    with open(path, "w", encoding="utf-8") as target:
        json.dump(value, target, indent=2, sort_keys=True)
        target.write("\n")
    path.chmod(0o600)


def run_checked(argv, cwd=None, env=None):
    return subprocess.check_output(argv, cwd=cwd, env=env, stderr=subprocess.PIPE, text=True).strip()


def git_env():
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env.update({"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_LFS_SKIP_SMUDGE": "1"})
    return env


def git(*args, cwd=None):
    return run_checked(["git", *args], cwd=cwd, env=git_env())


def verified_head():
    head = git("rev-parse", "HEAD", cwd=REPO)
    if head != BASELINE:
        raise RuntimeError(f"source HEAD {head} differs from frozen baseline {BASELINE}")
    if git("diff", "--name-only", "HEAD", "--", "cmd", "internal", "go.mod", "go.sum", cwd=REPO):
        raise RuntimeError("source code differs from frozen HEAD")


def snapshot_executable(source, destination):
    original = Path(source).expanduser().resolve(strict=True)
    if not original.is_file():
        raise RuntimeError(f"executable is not a regular file: {source}")
    shutil.copy2(original, destination)
    destination.chmod(0o700)
    return sha256(destination)


def prepare(args):
    verified_head()
    if hashlib.sha256(SEED.encode()).digest()[0] % 2 != 0:
        raise RuntimeError("frozen seed no longer selects Claude-first")
    if not TASK.is_file() or not RUBRIC.is_file() or not REVIEWER.is_file() or sha256(HIDDEN) != FROZEN_HIDDEN_SHA256:
        raise RuntimeError("frozen task, rubric, reviewer instructions, and hidden grader files are required")
    out = Path(args.out).expanduser().resolve()
    if out == REPO or REPO in out.parents:
        raise RuntimeError("output must be outside the source repository")
    if out.exists():
        raise RuntimeError("output directory already exists; choose a fresh directory")
    out.mkdir(parents=True, mode=0o700)
    snapshots = out / "snapshots"
    snapshots.mkdir(mode=0o700)
    kiln = snapshots / "kiln"
    claude = snapshots / "claude"
    kiln_hash = snapshot_executable(args.kiln_bin, kiln)
    claude_hash = snapshot_executable(args.claude_bin, claude)
    build_info = run_checked(["go", "version", "-m", str(kiln)])
    if f"vcs.revision={BASELINE}" not in build_info:
        raise RuntimeError("Kiln executable does not declare the frozen baseline commit")
    claude_version = run_checked([str(claude), "--version"])
    if "2.1.319" not in claude_version:
        raise RuntimeError(f"unexpected Claude CLI version: {claude_version}")
    workspaces = {}
    for repeat in (1, 2):
        for arm in ("claude", "kiln"):
            dest = out / "runs" / str(repeat) / arm
            dest.mkdir(parents=True, mode=0o700)
            project = dest / "project"
            git("clone", "--no-local", "--no-checkout", str(REPO), str(project))
            git("checkout", "--detach", BASELINE, cwd=project)
            git("remote", "remove", "origin", cwd=project)
            if git("rev-parse", "HEAD", cwd=project) != BASELINE or git("status", "--porcelain=v1", cwd=project):
                raise RuntimeError(f"clone is not a clean baseline: {project}")
            if git("remote", "-v", cwd=project):
                raise RuntimeError(f"clone still exposes a source remote: {project}")
            (dest / "home").mkdir(mode=0o700)
            (dest / "home" / ".claude").mkdir(mode=0o700)
            workspaces[f"{repeat}-{arm}"] = str(project)
    manifest = {
        "baseline": BASELINE,
        "task_sha256": sha256(TASK),
        "rubric_sha256": sha256(RUBRIC),
        "hidden_acceptance_sha256": sha256(HIDDEN),
        "reviewer_instructions_sha256": sha256(REVIEWER),
        "runner_sha256": sha256(__file__),
        "model": MODEL,
        "effort": "high",
        "turn_cap": TURN_CAP,
        "wall_cap_seconds": WALL_CAP_SECONDS,
        "seed": SEED,
        "schedule": [[1, "claude"], [1, "kiln"], [2, "kiln"], [2, "claude"]],
        "prior_reported_usd": PRIOR_REPORTED_USD,
        "total_cap_usd": TOTAL_CAP_USD,
        "kiln_sha256": kiln_hash,
        "claude_sha256": claude_hash,
        "claude_version": claude_version,
        "go_module_cache": args.go_mod_cache,
        "go_build_cache": args.go_build_cache,
        "go_cache_note": "Shared warmed host Go caches; write protection is not filesystem-enforced.",
        "workspaces": workspaces,
    }
    write_json(out / "manifest.json", manifest)
    print(f"prepared {out}; 4 clean clones, frozen task/rubric and two executable hashes; no model calls")


def credentials():
    path = Path.home() / ".harness" / "credentials.json"
    try:
        token = json.loads(path.read_text(encoding="utf-8"))["anthropic"]["access"]
    except (OSError, KeyError, ValueError, TypeError) as exc:
        raise RuntimeError("Anthropic OAuth credential is unavailable") from exc
    if not isinstance(token, str) or not token:
        raise RuntimeError("Anthropic OAuth credential is unavailable")
    return token


def model_env(run_dir, arm, manifest, token):
    env = {k: os.environ[k] for k in ("PATH", "LANG", "LC_ALL", "TERM", "SHELL") if k in os.environ}
    (run_dir / "tmp").mkdir(exist_ok=True, mode=0o700)
    env.update({
        "HOME": str(run_dir / "home"),
        "TMPDIR": str(run_dir / "tmp"),
        "CLAUDE_CONFIG_DIR": str(run_dir / "home" / ".claude"),
        "GOPROXY": "off",
        "GOFLAGS": "-mod=readonly",
        "GOTOOLCHAIN": "go1.26.6",
        "GOMODCACHE": manifest["go_module_cache"],
        "GOCACHE": manifest["go_build_cache"],
        "GOPATH": str(run_dir / "home" / "go"),
        "GOBIN": str(run_dir / "home" / "go" / "bin"),
        "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_CONFIG_NOSYSTEM": "1",
    })
    if arm == "kiln":
        env["ANTHROPIC_OAUTH_TOKEN"] = token
        env["HARNESS_LOG_DIR"] = str(run_dir / "logs")
        env["HARNESS_SESSIONS_DIR"] = str(run_dir / "sessions")
    else:
        env["CLAUDE_CODE_OAUTH_TOKEN"] = token
    return env


def reported_result(arm, stdout_path):
    if arm == "kiln":
        result = json.loads(stdout_path.read_text(encoding="utf-8"))
        if not result.get("ok"):
            raise RuntimeError("kiln result did not complete ok")
        cost = result.get("total_cost_usd")
        calls = result.get("num_tool_calls")
        turns = result.get("num_turns")
    else:
        results = []
        with open(stdout_path, encoding="utf-8") as stream:
            for line in stream:
                event = json.loads(line)
                if event.get("type") == "result":
                    results.append(event)
        if len(results) != 1 or results[0].get("is_error"):
            raise RuntimeError("Claude stream lacks one successful result event")
        cost = results[0].get("total_cost_usd")
        calls = None
        turns = results[0].get("num_turns")
    if not isinstance(cost, (int, float)) or isinstance(cost, bool) or not 0 < cost < TOTAL_CAP_USD:
        raise RuntimeError("run has unknown or invalid reported cost")
    return float(cost), calls, turns


def observed_routes(arm, run_dir, stdout_path):
    routes = set()
    if arm == "kiln":
        for log in (run_dir / "logs").glob("*.log"):
            routes.update(re.findall(r"phase session start[^\n]*model=([^ ]+)", log.read_text(errors="replace")))
            routes.update(re.findall(r"subagent model resolved[^\n]*model=([^ ]+)", log.read_text(errors="replace")))
    else:
        with open(stdout_path, encoding="utf-8") as stream:
            for line in stream:
                event = json.loads(line)
                if event.get("type") == "system" and event.get("subtype") == "init":
                    routes.add(event.get("model"))
                if event.get("type") == "assistant":
                    routes.add(event.get("message", {}).get("model"))
    return sorted(route for route in routes if route)


def capture_patch(project, run_dir):
    env = git_env()
    subprocess.check_call(["git", "-c", "core.hooksPath=/dev/null", "add", "-A"], cwd=project, env=env, stdout=subprocess.DEVNULL)
    with open(run_dir / "changes.patch", "wb") as patch:
        subprocess.check_call(["git", "-c", "core.hooksPath=/dev/null", "diff", "--cached", "--binary", BASELINE], cwd=project, env=env, stdout=patch)
    (run_dir / "git-status.txt").write_text(git("status", "--porcelain=v1", cwd=project) + "\n")


def run_one(out, manifest, repeat, arm, spend):
    remaining = TOTAL_CAP_USD - spend
    if arm == "claude" and remaining < CLAUDE_RUN_CAP_USD + CLAUDE_HEADROOM_USD:
        raise RuntimeError("remaining cap cannot reserve Claude run plus soft-cap headroom")
    kiln_budget = min(KILN_MAX_USD, remaining - CLAUDE_RUN_CAP_USD - CLAUDE_HEADROOM_USD)
    if arm == "kiln" and kiln_budget < KILN_FULL_CONTEXT_RESERVE_USD:
        raise RuntimeError("remaining cap cannot reserve a full-context kiln request")
    run_dir = out / "runs" / str(repeat) / arm
    project = run_dir / "project"
    if git("rev-parse", "HEAD", cwd=project) != BASELINE or git("status", "--porcelain=v1", cwd=project):
        raise RuntimeError("run workspace is no longer a clean baseline")
    settings = run_dir / "settings.json"
    if arm == "kiln":
        settings_data = {
            "requestBudget": {"maxUSD": round(kiln_budget, 6), "maxCalls": 100},
            "personalMemoryEnabled": False,
            "learningEnabled": False,
            "modelRoles": {"fast": "anthropic/" + MODEL, "heavy": "anthropic/" + MODEL},
            "permissions": {"deny": ["WebSearch"]},
        }
    else:
        settings_data = {"permissions": {"deny": ["WebSearch"]}}
    write_json(settings, settings_data)
    prompt = TASK.read_text(encoding="utf-8")
    if arm == "kiln":
        command = [str(out / "snapshots" / "kiln"), "-p", prompt, "--output-format", "json", "--model", "anthropic/" + MODEL, "--effort", "high", "--permission-mode", "bypassPermissions", "--max-turns", str(TURN_CAP), "--settings", str(settings)]
        stdout_path = run_dir / "stdout.json"
    else:
        command = [str(out / "snapshots" / "claude"), "-p", prompt, "--output-format", "stream-json", "--verbose", "--model", MODEL, "--effort", "high", "--permission-mode", "bypassPermissions", "--max-turns", str(TURN_CAP), "--max-budget-usd", str(CLAUDE_RUN_CAP_USD), "--no-session-persistence", "--strict-mcp-config", "--settings", str(settings)]
        stdout_path = run_dir / "stdout.jsonl"
    write_json(run_dir / "command.json", command)
    reservation = (CLAUDE_RUN_CAP_USD + CLAUDE_HEADROOM_USD) if arm == "claude" else kiln_budget
    attempt = {
        "repeat": repeat, "arm": arm, "status": "started", "reservation_usd": reservation,
        "task_sha256": sha256(TASK), "settings_sha256": sha256(settings),
        "binary_sha256": manifest[arm + "_sha256"],
    }
    write_json(run_dir / "attempt.json", attempt)
    token = credentials()
    env = model_env(run_dir, arm, manifest, token)
    start = time.monotonic()
    timed_out = False
    exit_code = None
    with open(stdout_path, "wb") as stdout, open(run_dir / "stderr.txt", "wb") as stderr:
        process = subprocess.Popen(command, cwd=project, env=env, stdout=stdout, stderr=stderr, start_new_session=True)
        try:
            exit_code = process.wait(timeout=WALL_CAP_SECONDS)
        except subprocess.TimeoutExpired:
            timed_out = True
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
    wall_ms = round((time.monotonic() - start) * 1000)
    attempt.update({"wall_ms": wall_ms, "exit_code": exit_code, "status": "timed_out" if timed_out else "exited"})
    try:
        capture_patch(project, run_dir)
        attempt["patch_bytes"] = (run_dir / "changes.patch").stat().st_size
    except Exception as exc:
        attempt["patch_capture_error"] = str(exc)
    write_json(run_dir / "attempt.json", attempt)
    if timed_out:
        raise RuntimeError(f"{arm} repeat {repeat} exceeded {WALL_CAP_SECONDS}s wall cap; stop all paid calls")
    if exit_code != 0:
        raise RuntimeError(f"{arm} repeat {repeat} exited {exit_code}; stop all paid calls")
    cost, calls, turns = reported_result(arm, stdout_path)
    if "patch_capture_error" in attempt:
        raise RuntimeError(f"patch capture failed: {attempt['patch_capture_error']}; stop all paid calls")
    attempt.update({"status": "completed", "reported_cost_usd": cost})
    write_json(run_dir / "attempt.json", attempt)
    row = {
        "repeat": repeat, "arm": arm, "reported_cost_usd": cost,
        "wall_ms": wall_ms, "exit_code": exit_code, "turns": turns,
        "tool_calls": calls, "observed_routes": observed_routes(arm, run_dir, stdout_path),
        "patch_bytes": (run_dir / "changes.patch").stat().st_size,
        "settings_sha256": sha256(settings), "task_sha256": sha256(TASK),
        "binary_sha256": manifest[arm + "_sha256"],
    }
    write_json(run_dir / "summary.json", row)
    return row


def execute(args):
    if not args.live:
        raise RuntimeError("paid execution requires explicit --live")
    out = Path(args.out).expanduser().resolve(strict=True)
    manifest = json.loads((out / "manifest.json").read_text(encoding="utf-8"))
    if (out / "stopped.json").exists():
        raise RuntimeError("cohort previously stopped; inspect it before any new paid calls")
    if (manifest["baseline"] != BASELINE or manifest["task_sha256"] != sha256(TASK)
            or manifest["rubric_sha256"] != sha256(RUBRIC)
            or manifest["hidden_acceptance_sha256"] != FROZEN_HIDDEN_SHA256
            or manifest["hidden_acceptance_sha256"] != sha256(HIDDEN)
            or manifest["reviewer_instructions_sha256"] != sha256(REVIEWER)
            or manifest["runner_sha256"] != sha256(__file__)):
        raise RuntimeError("frozen task, rubric, hidden grader, runner, or baseline changed")
    for arm in ("kiln", "claude"):
        if sha256(out / "snapshots" / arm) != manifest[arm + "_sha256"]:
            raise RuntimeError(f"{arm} executable changed after preparation")
    rows_file = out / "rows.jsonl"
    rows = [json.loads(line) for line in rows_file.read_text().splitlines()] if rows_file.exists() else []
    spent = PRIOR_REPORTED_USD + sum(row["reported_cost_usd"] for row in rows)
    completed = {(row["repeat"], row["arm"]) for row in rows}
    for repeat, arm in manifest["schedule"]:
        if (repeat, arm) in completed:
            continue
        try:
            row = run_one(out, manifest, repeat, arm, spent)
        except Exception as exc:
            attempt_path = out / "runs" / str(repeat) / arm / "attempt.json"
            attempt = json.loads(attempt_path.read_text(encoding="utf-8")) if attempt_path.exists() else {}
            write_json(out / "stopped.json", {
                "repeat": repeat, "arm": arm, "reason": str(exc),
                "reported_spend_before_failed_run_usd": spent,
                "unresolved_reservation_usd": attempt.get("reservation_usd"),
                "attempt": str(attempt_path),
            })
            raise
        spent += row["reported_cost_usd"]
        if spent > TOTAL_CAP_USD:
            write_json(out / "stopped.json", {"repeat": repeat, "arm": arm, "reason": "reported cap exceeded", "reported_spend_usd": spent})
            raise RuntimeError("reported cap exceeded; stop all paid calls")
        with open(rows_file, "a", encoding="utf-8") as target:
            target.write(json.dumps(row, sort_keys=True) + "\n")
        print(f"completed {arm} repeat {repeat}: ${row['reported_cost_usd']:.4f} reported, ${spent:.4f} cumulative; patch {row['patch_bytes']} bytes", flush=True)
    print(f"completed all four live runs; ${spent:.4f} total reported including earlier cohort")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    prep = subparsers.add_parser("prepare")
    prep.add_argument("--out", required=True)
    prep.add_argument("--kiln-bin", required=True)
    prep.add_argument("--claude-bin", required=True)
    prep.add_argument("--go-mod-cache", default=run_checked(["go", "env", "GOMODCACHE"]))
    prep.add_argument("--go-build-cache", default=run_checked(["go", "env", "GOCACHE"]))
    live = subparsers.add_parser("run")
    live.add_argument("--out", required=True)
    live.add_argument("--live", action="store_true")
    args = parser.parse_args()
    try:
        if args.command == "prepare":
            prepare(args)
        else:
            execute(args)
    except Exception as exc:
        print(f"comparison runner: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
