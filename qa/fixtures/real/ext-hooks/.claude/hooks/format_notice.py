#!/usr/bin/env python3
"""PostToolUse: pretends to format/lint whatever file was just touched."""
import json
import sys

try:
    payload = json.load(sys.stdin)
except Exception:
    payload = {}

path = (payload.get("tool_input") or {}).get("file_path") or (payload.get("tool_input") or {}).get("path") or "?"
print("formatter: reformatted %s" % path)
with open(".kiln-format-marker", "a") as f:
    f.write(path + "\n")
