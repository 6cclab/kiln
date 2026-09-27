#!/usr/bin/env python3
"""Create and list QA findings: one JSON file per finding under qa/findings/.

    scripts/qa/findings.py new --slug shift-enter-submits --scenario input/multiline \\
        --category bug --severity high --terminal iterm-dark \\
        --observed "..." --expected "..." --design-ref "Terminal.dc.html:344" \\
        --evidence qa/runs/.../03-typed.png
    scripts/qa/findings.py list [--status open] [--category style]
    scripts/qa/findings.py set <id> --status fixed --fix-files a.go,b.go --fix-test TestX \\
        --after qa/runs/.../after.png

A finding is a record of one fact, so it lives in its own timestamped file
and is updated in place as its status changes; there is no prose index.
"""

import argparse
import json
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DIR = ROOT / "qa" / "findings"
CATEGORIES = ("bug", "usability", "style", "design-drift")
SEVERITIES = ("high", "medium", "low")
STATUSES = ("open", "fixed", "wontfix")


def load_all():
    out = []
    for p in sorted(DIR.glob("*.json")):
        d = json.loads(p.read_text())
        d["_path"] = str(p.relative_to(ROOT))
        out.append(d)
    return out


def cmd_new(a):
    DIR.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    fid = "%s-%s" % (stamp, a.slug)
    rec = {
        "id": fid,
        "found_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "scenario": a.scenario,
        "step": a.step,
        "terminals": a.terminal or [],
        "category": a.category,
        "severity": a.severity,
        "title": a.title,
        "observed": a.observed,
        "expected": a.expected,
        "design_ref": a.design_ref,
        "evidence": a.evidence or [],
        "status": "open",
        "fix": None,
    }
    path = DIR / (fid + ".json")
    path.write_text(json.dumps(rec, indent=1, ensure_ascii=False) + "\n")
    print(path.relative_to(ROOT))


def cmd_list(a):
    rows = load_all()
    if a.status:
        rows = [r for r in rows if r["status"] == a.status]
    if a.category:
        rows = [r for r in rows if r["category"] == a.category]
    order = {s: i for i, s in enumerate(SEVERITIES)}
    rows.sort(key=lambda r: (order.get(r["severity"], 9), r["id"]))
    for r in rows:
        print("%-7s %-6s %-12s %-44s %s" % (r["status"], r["severity"], r["category"], r["id"][17:61], r["title"]))
    print("%d finding(s)" % len(rows), file=sys.stderr)


def cmd_set(a):
    matches = [p for p in DIR.glob("*.json") if a.id in p.stem]
    if len(matches) != 1:
        sys.exit("id %r matched %d findings" % (a.id, len(matches)))
    p = matches[0]
    rec = json.loads(p.read_text())
    if a.status:
        rec["status"] = a.status
    if a.severity:
        rec["severity"] = a.severity
    if a.fix_files or a.fix_test or a.after or a.note:
        fix = rec.get("fix") or {}
        if a.fix_files:
            fix["files"] = a.fix_files.split(",")
        if a.fix_test:
            fix["regression_test"] = a.fix_test
        if a.after:
            fix.setdefault("after_evidence", []).extend(a.after)
        if a.note:
            fix["note"] = a.note
        fix["updated_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        rec["fix"] = fix
    p.write_text(json.dumps(rec, indent=1, ensure_ascii=False) + "\n")
    print(p.relative_to(ROOT), rec["status"])


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    n = sub.add_parser("new")
    n.add_argument("--slug", required=True)
    n.add_argument("--title", required=True)
    n.add_argument("--scenario", required=True)
    n.add_argument("--step", default=None)
    n.add_argument("--terminal", action="append")
    n.add_argument("--category", required=True, choices=CATEGORIES)
    n.add_argument("--severity", required=True, choices=SEVERITIES)
    n.add_argument("--observed", required=True)
    n.add_argument("--expected", required=True)
    n.add_argument("--design-ref", default=None)
    n.add_argument("--evidence", action="append")
    n.set_defaults(func=cmd_new)

    l = sub.add_parser("list")
    l.add_argument("--status", choices=STATUSES)
    l.add_argument("--category", choices=CATEGORIES)
    l.set_defaults(func=cmd_list)

    s = sub.add_parser("set")
    s.add_argument("id")
    s.add_argument("--status", choices=STATUSES)
    s.add_argument("--severity", choices=SEVERITIES)
    s.add_argument("--fix-files")
    s.add_argument("--fix-test")
    s.add_argument("--after", action="append")
    s.add_argument("--note")
    s.set_defaults(func=cmd_set)

    a = ap.parse_args()
    a.func(a)


if __name__ == "__main__":
    main()
