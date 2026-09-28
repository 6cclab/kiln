#!/bin/sh
# PreToolUse(Bash): refuse to rebuild bin/kiln, or start another drive,
# while a real-terminal drive (scripts/qa/drive.py) is running. The drive
# launches bin/kiln for every scenario, so a rebuild mid-run tests a mix of
# two binaries and its results mean nothing.
cmd=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("command",""))')
case "$cmd" in
  *"make build"*|*"make qa"*|*"-o bin/kiln"*|*"-o ./bin/kiln"*) ;;
  *) exit 0 ;;
esac
if pgrep -f "scripts/qa/drive.py" >/dev/null 2>&1; then
  echo "A real-terminal drive (scripts/qa/drive.py) is running and uses bin/kiln. Wait for it to finish before rebuilding or starting another drive." >&2
  exit 2
fi
exit 0
