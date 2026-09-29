#!/usr/bin/env python3
"""PreToolUse: blocks edits/writes to anything under migrations/."""
import json
import sys

try:
    payload = json.load(sys.stdin)
except Exception:
    payload = {}

tool_input = payload.get("tool_input") or {}
path = tool_input.get("file_path") or tool_input.get("path") or ""

if "migrations/" in path.replace("\\", "/"):
    print("migrations/ is protected; open a migration PR reviewed by a human instead", file=sys.stderr)
    sys.exit(2)

sys.exit(0)
