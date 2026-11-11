#!/usr/bin/env python3
"""Independent black-box acceptance checks for task.md's memory import contract.

Usage: python3 hidden_acceptance.py --binary /absolute/path/to/kiln [--case NAME]
Each case uses a new HOME and Git project. No model or network call is made.
The output is one JSON object per case; the process exits nonzero on any failure.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time
import traceback


SECRET = "PRIVATE_IMPORT_CONTENT_DO_NOT_ECHO"


class Fixture:
    def __init__(self, binary):
        self.tmp = tempfile.TemporaryDirectory(prefix="kiln-import-accept-")
        self.base = Path(self.tmp.name)
        self.home = self.base / "home"
        self.home.mkdir()
        self.project = self.new_project("project")
        self.binary = str(Path(binary).resolve())
        self.env = dict(os.environ, HOME=str(self.home), XDG_CONFIG_HOME=str(self.home / ".config"))
        self.db = self.home / ".kiln" / "memory.db"

    def close(self):
        self.tmp.cleanup()

    def new_project(self, name):
        path = self.base / name
        path.mkdir()
        subprocess.run(["git", "init", "-q", str(path)], check=True, capture_output=True)
        return path

    def call(self, *args, project=None):
        return subprocess.run(
            [self.binary, "memory", *map(str, args)],
            cwd=project or self.project,
            env=self.env,
            text=True,
            capture_output=True,
            timeout=20,
        )

    def ok(self, *args, project=None):
        result = self.call(*args, project=project)
        assert result.returncode == 0, f"command failed: {args}: {result.stderr[:300]}"
        return result

    def fail(self, *args, project=None, secret=SECRET):
        result = self.call(*args, project=project)
        assert result.returncode != 0, f"invalid command succeeded: {args}"
        assert result.stderr.strip(), f"failure lacked diagnostic: {args}"
        assert secret not in result.stderr, "diagnostic leaked imported memory text"
        assert not result.stdout.strip().startswith("{"), "failure emitted success JSON"
        return result

    def export(self, global_scope=False, project=None):
        args = ("global", "export") if global_scope else ("export",)
        return json.loads(self.ok(*args, project=project).stdout)

    def input(self, records, name="input.json", raw=None):
        path = self.base / name
        path.write_bytes((json.dumps(records) if raw is None else raw).encode() if isinstance(raw, str) or raw is None else raw)
        return path

    def import_ok(self, records, *flags, global_scope=False):
        path = self.input(records)
        prefix = ("global", "import") if global_scope else ("import",)
        result = self.ok(*prefix, path, *flags)
        value = json.loads(result.stdout)
        required = {"inserted", "replaced", "skipped", "dry_run"}
        assert isinstance(value, dict) and required <= set(value), value
        assert all(type(value[k]) is int and value[k] >= 0 for k in ("inserted", "replaced", "skipped")), value
        assert type(value["dry_run"]) is bool, value
        return value

    def import_fail(self, records, *flags, global_scope=False):
        path = self.input(records)
        prefix = ("global", "import") if global_scope else ("import",)
        return self.fail(*prefix, path, *flags)

    def snapshot(self):
        if not self.db.exists():
            return None
        with sqlite3.connect(self.db) as conn:
            return tuple(
                tuple(conn.execute(f"SELECT * FROM {table} ORDER BY rowid").fetchall())
                for table in ("memories", "memory_sources", "memory_search")
            )

    def anchored(self):
        self.ok("remember", "anchor", "Existing anchor")
        return self.snapshot()


def rec(id=1, key="new", text="Imported value", kind="observation", **more):
    value = dict(id=id, scope="/origin/project/.git", key=key, kind=kind, text=text,
                 evidence="Exported user memory", created=1, expires=0, supersedes=0)
    value.update(more)
    return value


def source(path, data, origin="/machine-that-is-not-here/project"):
    return dict(root=origin, path=f"{origin}/{path}", sha256=hashlib.sha256(data).hexdigest())


def expect_counts(result, inserted, replaced, skipped, dry_run):
    expected = dict(inserted=inserted, replaced=replaced, skipped=skipped, dry_run=dry_run)
    assert all(result[key] == value for key, value in expected.items()), (result, expected)


def unchanged_after_failure(f, records, *flags, global_scope=False):
    before = f.anchored()
    f.import_fail(records, *flags, global_scope=global_scope)
    assert f.snapshot() == before, "failed import changed memory, source, or FTS tables"


def case_existing_workflow(f):
    f.ok("remember", "anchor", "Existing anchor")
    assert [r["key"] for r in f.export()] == ["anchor"]
    f.ok("global", "remember", "format", "Use JSON")
    assert [r["key"] for r in f.export(global_scope=True)] == ["format"]
    assert [r["key"] for r in f.export()] == ["anchor"]


def case_empty_and_insert(f):
    expect_counts(f.import_ok([]), 0, 0, 0, False)
    result = f.import_ok([rec()])
    expect_counts(result, 1, 0, 0, False)
    found = f.export()
    assert len(found) == 1 and found[0]["key"] == "new" and found[0]["text"] == "Imported value"
    assert found[0]["scope"] != "/origin/project/.git"
    assert found[0]["id"] > 0 and found[0]["created"] > 1 and found[0].get("supersedes", 0) == 0


def case_history_collapse(f):
    values = [rec(9, "alpha", "latest"), rec(3, "alpha", "old", supersedes=2), rec(4, "beta", "only")]
    assert f.import_ok(values)["inserted"] == 2
    found = {r["key"]: r for r in f.export()}
    assert len(found) == 2 and found["alpha"]["text"] == "latest" and found["beta"]["text"] == "only"
    assert all(r.get("supersedes", 0) == 0 for r in found.values())


def case_input_id_rejections(f):
    for values in ([rec(id=0)], [rec(id=-1)], [rec(id=1), rec(id=1, key="other")]):
        unchanged_after_failure(f, values)


def case_invalid_history_rejected(f):
    values = [rec(1, text=" "), rec(2, text="valid")]
    unchanged_after_failure(f, values)
    values = [rec(1, kind="unrecognized"), rec(2)]
    unchanged_after_failure(f, values)


def case_field_and_expiry_limits(f):
    for value in (rec(key="x" * 257), rec(text="x" * 8193), rec(evidence="x" * 2049), rec(expires=-1)):
        unchanged_after_failure(f, [value])


def case_json_shape(f):
    before = f.anchored()
    for raw in ("{", "null", "{}", '[] {}', '[{"id":1,}]'):
        f.fail("import", f.input(None, raw=raw))
        assert f.snapshot() == before


def case_unknown_fields(f):
    unchanged_after_failure(f, [rec(extra_unknown=1)])
    value = rec(sources=[dict(source("a.txt", b"a"), unexpected=1)])
    unchanged_after_failure(f, [value])


def case_size_and_count_bounds(f):
    before = f.anchored()
    big = f.base / "large.json"
    big.write_bytes(b"[" + b" " * (4 * 1024 * 1024) + b"]")
    f.fail("import", big)
    assert f.snapshot() == before
    f.import_fail([rec(i, key=f"key{i}") for i in range(1, 1002)])
    assert f.snapshot() == before


def case_cli_args(f):
    before = f.anchored()
    value = f.input([rec()])
    for args in (("import",), ("import", value, "extra"), ("import", value, "--not-a-flag"),
                 ("import", value, "--on-conflict"), ("import", value, "--on-conflict", "merge"),
                 ("import", "--dry-run")):
        f.fail(*args)
        assert f.snapshot() == before


def case_project_scope_isolation(f):
    other = f.new_project("other")
    f.import_ok([rec(scope="user:preferences")])
    assert [r["key"] for r in f.export()] == ["new"]
    assert f.export(project=other) == []
    assert f.export(global_scope=True) == []


def case_global_preferences_only(f):
    result = f.import_ok([rec(kind="preference", scope="other/project")], global_scope=True)
    assert result["inserted"] == 1
    assert f.export() == []
    found = f.export(global_scope=True)
    assert len(found) == 1 and found[0]["scope"] == "user:preferences"
    assert found[0]["kind"] == "preference"
    before = f.snapshot()
    f.import_fail([rec(kind="fact", key="bad")], global_scope=True)
    assert f.snapshot() == before


def case_global_history_invalid(f):
    values = [rec(1, kind="observation"), rec(2, kind="preference")]
    unchanged_after_failure(f, values, global_scope=True)
    values = [rec(1, kind="preference", sources=[source("x", b"x")]), rec(2, kind="preference")]
    unchanged_after_failure(f, values, global_scope=True)


def case_dry_run_and_conflicts(f):
    before = f.anchored()
    result = f.import_ok([rec(key="anchor", text="new"), rec(2, key="fresh")],
                         "--dry-run", "--on-conflict", "skip")
    expect_counts(result, 1, 0, 1, True)
    assert f.snapshot() == before
    result = f.import_ok([rec(key="anchor", text="new"), rec(2, key="fresh")],
                         "--dry-run", "--on-conflict", "replace")
    expect_counts(result, 1, 1, 0, True)
    assert f.snapshot() == before


def case_default_conflict_atomic(f):
    before = f.anchored()
    f.import_fail([rec(key="fresh"), rec(2, key="anchor")])
    assert f.snapshot() == before


def case_skip_preserves_local(f):
    f.anchored()
    before = f.export()
    result = f.import_ok([rec(key="anchor", text="replacement"), rec(2, key="fresh")], "--on-conflict", "skip")
    expect_counts(result, 1, 0, 1, False)
    found = f.export()
    assert [r for r in found if r["key"] == "anchor"] == before
    assert len([r for r in found if r["key"] == "fresh"]) == 1


def case_replace_lineage(f):
    f.anchored()
    old = f.export()[0]
    result = f.import_ok([rec(id=900, key="anchor", text="replacement", created=5,
                              scope="user:preferences", supersedes=700)], "--on-conflict", "replace")
    expect_counts(result, 0, 1, 0, False)
    versions = [r for r in f.export() if r["key"] == "anchor"]
    assert len(versions) == 2 and versions[0] == old
    new = versions[1]
    assert new["id"] != 900 and new["id"] > old["id"] and new["supersedes"] == old["id"]
    assert new["created"] >= old["created"] and new["scope"] == old["scope"]
    f.ok("restore", str(old["id"]))
    assert f.export()[-1]["text"] == "Existing anchor"


def case_expired_active_conflict(f):
    f.anchored()
    with sqlite3.connect(f.db) as db:
        db.execute("UPDATE memories SET expires=1 WHERE key='anchor' AND active=1")
    before = f.snapshot()
    f.import_fail([rec(key="anchor")])
    assert f.snapshot() == before


def case_stale_active_conflict(f):
    path = f.project / "bound.txt"
    path.write_text("original")
    f.ok("remember", "anchor", "local", "bound.txt")
    path.write_text("changed")
    before = f.snapshot()
    f.import_fail([rec(key="anchor")])
    assert f.snapshot() == before


def case_source_rebind_and_freshness(f):
    data = b"destination contents\n"
    (f.project / "bound.txt").write_bytes(data)
    (f.project / "relative.txt").write_bytes(data)
    old_root = "/nonexistent-old-machine/project"
    absolute = rec(sources=[source("bound.txt", data, old_root)])
    relative = rec(2, key="relative", sources=[dict(source("relative.txt", data, old_root), path="relative.txt")])
    assert f.import_ok([absolute, relative])["inserted"] == 2
    found = {r["key"]: r for r in f.export()}
    for key, path in (("new", "bound.txt"), ("relative", "relative.txt")):
        stored = found[key]["sources"][0]
        assert stored["root"] == str(f.project.resolve())
        assert stored["path"] == str((f.project / path).resolve())
        assert stored["sha256"] == hashlib.sha256(data).hexdigest()
    (f.project / "bound.txt").write_text("changed")
    assert len(f.export()) == 2, "export should retain stale historical data"


def case_source_failures_atomic(f):
    (f.project / "bound.txt").write_bytes(b"actual")
    before = f.anchored()
    variants = [
        source("missing.txt", b"x"),
        source("bound.txt", b"wrong"),
        dict(source("bound.txt", b"actual"), sha256="not-a-sha256"),
        dict(source("bound.txt", b"actual"), sha256="0" * 64),
    ]
    for src in variants:
        f.import_fail([rec(sources=[src])])
        assert f.snapshot() == before


def case_source_path_boundaries(f):
    data = b"same"
    (f.project / "ok.txt").write_bytes(data)
    outside = f.base / "outside.txt"
    outside.write_bytes(data)
    (f.project / "redirect.txt").symlink_to(outside)
    original = "/old/project"
    variants = [
        dict(source("ok.txt", data, original), path=original + "/../secret.txt"),
        dict(source("ok.txt", data, original), path="/etc/passwd"),
        dict(source("ok.txt", data, original), root="relative/origin"),
        dict(source("ok.txt", data, original), path="../secret.txt"),
        source("redirect.txt", data, original),
    ]
    before = f.anchored()
    for src in variants:
        f.import_fail([rec(sources=[src])])
        assert f.snapshot() == before


def case_source_kind_and_limits(f):
    data = b"same"
    (f.project / "regular.txt").write_bytes(data)
    (f.project / "directory").mkdir()
    (f.project / "too-large.txt").write_bytes(b"x" * ((1 << 20) + 1))
    before = f.anchored()
    variants = [
        [source("regular.txt", data)] * 9,
        [source("directory", data)],
        [source("too-large.txt", b"x" * ((1 << 20) + 1))],
    ]
    for sources in variants:
        f.import_fail([rec(sources=sources)])
        assert f.snapshot() == before


def case_stale_historical_source(f):
    data = b"old"
    values = [rec(1, sources=[source("missing.txt", data)]), rec(2, text="selected")]
    unchanged_after_failure(f, values)


def case_rollback_on_memory_write(f):
    before = f.anchored()
    with sqlite3.connect(f.db) as db:
        db.execute("CREATE TRIGGER abort_import BEFORE INSERT ON memories "
                   "WHEN NEW.key IN ('first','second') AND "
                   "(SELECT COUNT(*) FROM memories WHERE key IN ('first','second'))=1 "
                   "BEGIN SELECT RAISE(ABORT, 'injected write failure'); END")
    f.import_fail([rec(1, key="first"), rec(2, key="second")])
    assert f.snapshot() == before


def case_rollback_on_replace(f):
    before = f.anchored()
    old_id = f.export()[0]["id"]
    with sqlite3.connect(f.db) as db:
        db.execute("CREATE TRIGGER abort_replace BEFORE INSERT ON memories "
                   "WHEN NEW.key IN ('anchor','fresh') AND "
                   f"(SELECT COUNT(*) FROM memories WHERE id>{old_id})=1 "
                   "BEGIN SELECT RAISE(ABORT, 'injected write failure'); END")
    f.import_fail([rec(1, key="anchor", text="replacement"), rec(2, key="fresh")],
                  "--on-conflict", "replace")
    assert f.snapshot() == before, "failed replacement changed old active row or search/source tables"


def case_rollback_on_source_write(f):
    data = b"bound"
    (f.project / "bound.txt").write_bytes(data)
    before = f.anchored()
    with sqlite3.connect(f.db) as db:
        db.execute("CREATE TRIGGER abort_source BEFORE INSERT ON memory_sources BEGIN SELECT RAISE(ABORT, 'injected source failure'); END")
    f.import_fail([rec(1, key="first"), rec(2, key="second", sources=[source("bound.txt", data)])])
    assert f.snapshot() == before


def case_cancel_waiting_for_database(f):
    before = f.anchored()
    value = f.input([rec(1, key="first"), rec(2, key="second")])
    lock = sqlite3.connect(f.db)
    lock.execute("BEGIN IMMEDIATE")
    process = subprocess.Popen([f.binary, "memory", "import", str(value)], cwd=f.project,
                               env=f.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        time.sleep(0.25)
        assert process.poll() is None, "import did not wait for the database lock"
        process.terminate()
        process.communicate(timeout=5)
        assert process.returncode != 0, "cancelled import succeeded"
    finally:
        if process.poll() is None:
            process.kill()
            process.communicate(timeout=5)
        lock.rollback()
        lock.close()
    assert f.snapshot() == before, "cancelled import changed destination tables"


def case_diagnostics_do_not_leak(f):
    before = f.anchored()
    value = rec(text=SECRET, kind="invalid-kind")
    f.import_fail([value])
    assert f.snapshot() == before


CASES = {name.removeprefix("case_"): value for name, value in sorted(globals().copy().items())
         if name.startswith("case_") and callable(value)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True)
    parser.add_argument("--case", choices=CASES)
    args = parser.parse_args()
    if not Path(args.binary).is_file():
        parser.error("--binary must name an existing kiln binary")
    failed = 0
    for name, test in CASES.items():
        if args.case and name != args.case:
            continue
        f = Fixture(args.binary)
        started = time.monotonic()
        try:
            # Negative cases must not pass merely because the baseline lacks
            # the import command. The empty valid import is a capability probe.
            if name != "existing_workflow":
                expect_counts(f.import_ok([]), 0, 0, 0, False)
            test(f)
            result = dict(case=name, passed=True, seconds=round(time.monotonic() - started, 3))
        except Exception as exc:
            failed += 1
            result = dict(case=name, passed=False, seconds=round(time.monotonic() - started, 3),
                          error=f"{type(exc).__name__}: {exc}")
        finally:
            f.close()
        print(json.dumps(result, sort_keys=True), flush=True)
    return int(failed != 0)


if __name__ == "__main__":
    sys.exit(main())
