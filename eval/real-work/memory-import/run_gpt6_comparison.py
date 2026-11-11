#!/usr/bin/env python3
"""Frozen GPT-6-Sol comparison; only run --live invokes models.

ChatGPT authentication in private homes. Costs are catalog estimates, not invoices.
Unknown usage stops the cohort; never reuse this starting ledger for a new cohort.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time
import run_comparison as common

BASELINE = common.BASELINE
HARNESS_REVISION = "d4d870d1365de02b6066e2036171d98ec61564ba"
MODEL = "gpt-6-sol"
PRIOR = 33.85363025
TOTAL = 50.0
MAX_RUN = 14.0
WALL_SECONDS = 1200
# Kiln's conservative reserve: 272k*(4 + 2*5) + 128k*15 per million.
# Codex permits one worker in addition to the primary thread.
REQUEST_RESERVE = 5.728
CODEX_HEADROOM = 2 * REQUEST_RESERVE
SCHEDULE = [[1, "codex"], [1, "kiln"], [2, "kiln"], [2, "codex"]]
HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
CODEX_BIN = Path.home() / ".nvm/versions/node/v24.13.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin"
CONFIG = """model = "gpt-6-sol"
model_reasoning_effort = "high"
model_context_window = 272000
web_search = "disabled"
approval_policy = "never"
sandbox_mode = "danger-full-access"
[agents]
max_concurrent_threads_per_session = 1
default_subagent_model = "gpt-6-sol"
default_subagent_reasoning_effort = "high"
[features]
apps = false
remote_plugin = false
memories = false
[analytics]
enabled = false
"""


def write(path, value):
    common.write_json(path, value)


def prepare(out):
    if out.exists():
        raise RuntimeError("choose a fresh cohort directory")
    out.mkdir(mode=0o700)
    snap = out / "snapshots"
    snap.mkdir(mode=0o700)
    common.snapshot_executable("/tmp/kiln-fidelity-rerun-bin", snap / "kiln")
    info = common.run_checked(["go", "version", "-m", str(snap / "kiln")])
    if "vcs.revision=" + HARNESS_REVISION not in info:
        raise RuntimeError("Kiln build revision mismatch")
    for name in ("codex", "codex-code-mode-host"):
        common.snapshot_executable(CODEX_BIN / name, snap / name)
    version = common.run_checked([str(snap / "codex"), "--version"])
    if version != "codex-cli 0.167.1":
        raise RuntimeError("unexpected Codex version")
    for n, arm in SCHEDULE:
        run = out / "runs" / str(n) / arm
        run.mkdir(parents=True, mode=0o700)
        project = run / "project"
        common.git("init", "-q", str(project))
        common.git("fetch", "--depth=1", str(REPO), BASELINE, cwd=project)
        common.git("checkout", "--detach", "FETCH_HEAD", cwd=project)
        if common.git("rev-parse", "HEAD", cwd=project) != BASELINE or common.git("status", "--porcelain", cwd=project):
            raise RuntimeError("baseline checkout invalid")
        (run / "home").mkdir(mode=0o700)
        (run / "tmp").mkdir(mode=0o700)
    manifest = dict(
        baseline=BASELINE, harness_revision=HARNESS_REVISION, model=MODEL,
        effort="high", context_window=272000, codex_version=version, schedule=SCHEDULE,
        prior_reported_usd=PRIOR, total_cap_usd=TOTAL, max_run_reservation_usd=MAX_RUN,
        codex_outstanding_headroom_usd=CODEX_HEADROOM, wall_cap_seconds=WALL_SECONDS,
        task_sha256=common.sha256(common.TASK), rubric_sha256=common.sha256(common.RUBRIC),
        hidden_acceptance_sha256=common.sha256(common.HIDDEN),
        runner_sha256=common.sha256(__file__), common_runner_sha256=common.sha256(common.__file__),
        binaries={name: common.sha256(snap / name) for name in ("kiln", "codex", "codex-code-mode-host")},
        go_module_cache=common.run_checked(["go", "env", "GOMODCACHE"]),
        go_build_cache=common.run_checked(["go", "env", "GOCACHE"]),
        pricing=dict(input=2, cached_input=.2, cache_write=2.5, output=10,
                     long_input=4, long_cached_input=.4, long_cache_write=5, long_output=15,
                     long_threshold=272000, unit="USD per million tokens"),
        price_source="https://developers.openai.com/api/docs/models/gpt-6-sol",
        judges=3, judge_model="gpt-6-sol",
        caveats=["ChatGPT-authenticated usage; catalog-equivalent cost, not an invoice.",
                 "Codex watchdog is external and approximate; unknown failures retain reservation.",
                 "Native delegation allowed with one concurrent worker; GPT-6-Sol worker default.",
                 "No personal memory, user plugins, or skills copied.",
                 "Model runs have baseline-only shallow repositories, excluding later solutions."])
    write(out / "manifest.json", manifest)
    print("Prepared four baseline-only clones and frozen executables; no model calls.", flush=True)


def environment(run, manifest):
    env = {k: os.environ[k] for k in ("PATH", "LANG", "LC_ALL", "TERM", "SHELL") if k in os.environ}
    env.update(HOME=str(run / "home"), TMPDIR=str(run / "tmp"), CODEX_HOME=str(run / "home/.codex"),
        CLAUDE_CONFIG_DIR=str(run / "home/.claude"), XDG_CONFIG_HOME=str(run / "home/.config"),
        GOPROXY="off", GOFLAGS="-mod=readonly", GOTOOLCHAIN="go1.26.6",
        GOMODCACHE=manifest["go_module_cache"], GOCACHE=manifest["go_build_cache"],
        GOPATH=str(run / "home/go"), GOBIN=str(run / "home/go/bin"),
        GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1",
        HARNESS_LOG_DIR=str(run / "logs"), HARNESS_SESSIONS_DIR=str(run / "sessions"))
    return env


def install_auth(run, arm):
    # Same current access token in both formats. Never print it or rotate the shared login.
    auth = json.loads((Path.home() / ".codex/auth.json").read_text())
    token = auth["tokens"]["access_token"]
    segment = token.split(".")[1]
    claims = json.loads(base64.urlsafe_b64decode(segment + "=" * (-len(segment) % 4)))
    if claims["exp"] < time.time() + 1800:
        raise RuntimeError("existing token too near expiry")
    if arm == "codex":
        folder = run / "home/.codex"
        folder.mkdir(mode=0o700)
        private = dict(auth)
        private["tokens"] = dict(auth["tokens"])
        private["tokens"]["refresh_token"] = ""
        write(folder / "auth.json", private)
        shutil.copyfile(Path.home() / ".codex/models_cache.json", folder / "models_cache.json")
    else:
        folder = run / "home/.harness"
        folder.mkdir(mode=0o700)
        write(folder / "credentials.json", {"openai-codex": {"type": "oauth", "access": token,
            "refresh": "", "expires": int(claims["exp"] * 1000) - 300000}})


def cost(usage, long=False):
    inp = usage.get("input_tokens", 0)
    cached = usage.get("cached_input_tokens", 0)
    writes = usage.get("cache_write_input_tokens", 0)
    output = usage.get("output_tokens", 0)
    if min(inp, cached, writes, output) < 0 or cached + writes > inp:
        raise RuntimeError("invalid token usage")
    rates = (4, .4, 5, 15) if long else (2, .2, 2.5, 10)
    return ((inp - cached - writes) * rates[0] + cached * rates[1] + writes * rates[2] + output * rates[3]) / 1e6


def codex_usage(run):
    rows, models = [], set()
    for path in (run / "home/.codex/sessions").rglob("*.jsonl"):
        previous, estimated, requests = {}, 0., 0
        for line in path.read_text().splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            payload = event.get("payload", {})
            if event.get("type") == "turn_context" and payload.get("model"):
                models.add(payload["model"])
            if event.get("type") != "event_msg" or payload.get("type") != "token_count":
                continue
            info = payload.get("info") or {}
            total = info.get("total_token_usage")
            if not total or total == previous:
                continue
            delta = {k: total.get(k, 0) - previous.get(k, 0) for k in
                ("input_tokens", "cached_input_tokens", "cache_write_input_tokens", "output_tokens")}
            if min(delta.values()) < 0:
                raise RuntimeError("usage counter reset; reconcile before continuing")
            estimated += cost(delta, long=(info.get("last_token_usage") or delta).get("input_tokens", 0) > 272000)
            previous = total
            requests += 1
        if previous:
            rows.append(dict(session=path.name, total=previous, estimated_cost_usd=estimated, requests=requests))
    return dict(sessions=rows, models=sorted(models), estimated_cost_usd=sum(r["estimated_cost_usd"] for r in rows),
                observed_requests=sum(r["requests"] for r in rows))


def terminate(process):
    if process.poll() is not None:
        return
    os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()


def run_one(out, manifest, n, arm, spent):
    allowance = min(MAX_RUN, TOTAL - spent)
    required = CODEX_HEADROOM if arm == "codex" else REQUEST_RESERVE
    if allowance < required:
        raise RuntimeError("insufficient remaining reservation; no further model calls")
    run = out / "runs" / str(n) / arm
    project = run / "project"
    if (run / "attempt.json").exists():
        raise RuntimeError("attempt exists; do not repeat paid calls")
    install_auth(run, arm)
    env = environment(run, manifest)
    prompt = common.TASK.read_text()
    if arm == "kiln":
        settings = {"requestBudget": {"maxUSD": int(allowance * 1e6) / 1e6, "maxCalls": 100},
            "personalMemoryEnabled": False, "learningEnabled": False,
            "modelRoles": {"fast": "openai-codex/" + MODEL, "heavy": "openai-codex/" + MODEL},
            "orchestration": {"maxConcurrent": 1, "maxDepth": 1}, "permissions": {"deny": ["WebSearch"]}}
        write(run / "settings.json", settings)
        command = [str(out / "snapshots/kiln"), "-p", prompt, "--output-format", "json", "--model",
            "openai-codex/" + MODEL, "--effort", "high", "--permission-mode", "bypassPermissions",
            "--max-turns", "80", "--settings", str(run / "settings.json")]
        stdout = run / "stdout.json"
    else:
        config = run / "home/.codex/config.toml"
        config.write_text(CONFIG)
        config.chmod(0o600)
        command = [str(out / "snapshots/codex"), "exec", "--strict-config", "--json", "--color", "never",
            "--model", MODEL, "--dangerously-bypass-approvals-and-sandbox", "-C", str(project), "-"]
        stdout = run / "stdout.jsonl"
    write(run / "command.json", command)
    attempt = dict(arm=arm, repeat=n, status="started", reservation_usd=allowance,
        task_sha256=manifest["task_sha256"], binary_sha256=manifest["binaries"][arm])
    write(run / "attempt.json", attempt)
    start, stop, process = time.monotonic(), None, None
    try:
        with stdout.open("wb") as outstream, (run / "stderr.txt").open("wb") as errstream:
            process = subprocess.Popen(command, cwd=project, env=env,
                stdin=subprocess.PIPE if arm == "codex" else subprocess.DEVNULL,
                stdout=outstream, stderr=errstream, start_new_session=True)
            if arm == "codex":
                process.stdin.write(prompt.encode())
                process.stdin.close()
            while process.poll() is None:
                if time.monotonic() - start > WALL_SECONDS:
                    stop = "wall cap"
                if arm == "codex":
                    usage = codex_usage(run)
                    if set(usage["models"]) - {MODEL}:
                        stop = "unexpected model; audit usage"
                    if usage["estimated_cost_usd"] + CODEX_HEADROOM > allowance:
                        stop = "conservative cost watchdog"
                    if usage["observed_requests"] >= 100:
                        stop = "request-count watchdog"
                if stop:
                    terminate(process)
                    break
                time.sleep(.5)
        attempt.update(exit_code=process.returncode, wall_ms=round((time.monotonic() - start) * 1000),
            status="stopped" if stop else "exited", stop_reason=stop)
        common.capture_patch(project, run)
        attempt["patch_bytes"] = (run / "changes.patch").stat().st_size
        attempt["patch_sha256"] = common.sha256(run / "changes.patch")
        if arm == "codex":
            usage = codex_usage(run)
            write(run / "usage.json", usage)
            events = [json.loads(l) for l in stdout.read_text().splitlines() if l.startswith("{")]
            finals = [e for e in events if e.get("type") == "turn.completed"]
            if process.returncode != 0 or stop or not finals or not usage["sessions"]:
                raise RuntimeError("Codex did not complete with auditable usage; stop cohort")
            attempt["reported_cost_usd"] = usage["estimated_cost_usd"]
            attempt["cost_basis"] = "catalog-equivalent from per-session token counters"
        else:
            response = json.loads(stdout.read_text())
            if process.returncode != 0 or stop or not response.get("ok"):
                raise RuntimeError("Kiln did not complete successfully; stop cohort")
            attempt["reported_cost_usd"] = response["total_cost_usd"]
            attempt["cost_basis"] = "Kiln catalog cost"
        if not 0 < attempt["reported_cost_usd"] <= allowance:
            raise RuntimeError("invalid cost or reservation exceeded")
        attempt["status"] = "completed"
        write(run / "summary.json", attempt)
        return attempt
    finally:
        if process is not None:
            terminate(process)
        write(run / "attempt.json", attempt)
        for path in (run / "home/.codex/auth.json", run / "home/.harness/credentials.json"):
            path.unlink(missing_ok=True)


def execute(out):
    manifest = json.loads((out / "manifest.json").read_text())
    if manifest["runner_sha256"] != common.sha256(__file__) or manifest["common_runner_sha256"] != common.sha256(common.__file__):
        raise RuntimeError("frozen runner changed")
    if manifest["task_sha256"] != common.sha256(common.TASK) or manifest["hidden_acceptance_sha256"] != common.sha256(common.HIDDEN):
        raise RuntimeError("frozen evaluation changed")
    if (out / "stopped.json").exists():
        raise RuntimeError("cohort stopped; audit before further calls")
    for name, digest in manifest["binaries"].items():
        if common.sha256(out / "snapshots" / name) != digest:
            raise RuntimeError("executable changed")
    rows_path = out / "rows.jsonl"
    rows = [json.loads(l) for l in rows_path.read_text().splitlines()] if rows_path.exists() else []
    spent = PRIOR + sum(r["reported_cost_usd"] for r in rows)
    done = {(r["repeat"], r["arm"]) for r in rows}
    for n, arm in SCHEDULE:
        if (n, arm) in done:
            continue
        try:
            row = run_one(out, manifest, n, arm, spent)
        except Exception as exc:
            write(out / "stopped.json", dict(repeat=n, arm=arm, reason=str(exc),
                reported_spend_before_failed_run_usd=spent))
            raise
        spent += row["reported_cost_usd"]
        if spent > TOTAL:
            raise RuntimeError("total cap exceeded; no further calls")
        with rows_path.open("a") as stream:
            stream.write(json.dumps(row) + chr(10))
        print(f"completed {arm} {n}: USD {row['reported_cost_usd']:.6f}, {row['wall_ms']/1000:.1f}s; cumulative USD {spent:.6f}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["prepare", "run"])
    parser.add_argument("--out", required=True)
    parser.add_argument("--live", action="store_true")
    args = parser.parse_args()
    out = Path(args.out).expanduser().resolve()
    if args.command == "prepare":
        prepare(out)
    elif args.live:
        execute(out)
    else:
        raise RuntimeError("paid execution requires --live")


if __name__ == "__main__":
    main()
