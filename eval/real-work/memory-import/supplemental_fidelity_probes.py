#!/usr/bin/env python3
"""Post-review malformed-JSON probes for the fidelity follow-up.

Separate from the frozen acceptance suite; run uniformly on all four candidates.
No model calls, repairs, or real user data.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def probe(binary, name, content):
    with tempfile.TemporaryDirectory(prefix="kfr-json-") as directory:
        root = Path(directory)
        home, project = root / "home", root / "project"
        home.mkdir()
        project.mkdir()
        env = {k: os.environ[k] for k in ("PATH", "LANG", "TERM") if k in os.environ}
        env.update(HOME=str(home), GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
        subprocess.run(["git", "init", "-q", str(project)], env=env, check=True, capture_output=True)
        source = root / "input.json"
        source.write_text(content)
        result = subprocess.run([str(binary), "memory", "import", str(source)], cwd=project,
                                env=env, text=True, capture_output=True, timeout=20)
        passed = result.returncode != 0 and bool(result.stderr.strip()) and not result.stdout.strip().startswith("{")
        return dict(probe=name, status="passed" if passed else "failed", exit_code=result.returncode,
                    diagnostic=result.stderr[:500], stdout=result.stdout[:500])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    args = parser.parse_args()
    binary = Path(args.binary).resolve(strict=True)
    outcomes = [probe(binary, name, content) for name, content in (
        ("reject_trailing_array_close", "[]]"),
        ("reject_trailing_object_close", "[]}"),
        ("reject_trailing_json_value", "[] []"),
    )]
    for result in outcomes:
        print(json.dumps(result, sort_keys=True))
    return int(any(result["status"] == "failed" for result in outcomes))


if __name__ == "__main__":
    raise SystemExit(main())
