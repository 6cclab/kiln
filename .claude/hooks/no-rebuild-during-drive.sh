#!/bin/sh
# PreToolUse(Bash): refuse to rebuild bin/kiln, or start another drive,
# while a real-terminal drive (scripts/qa/drive.py) is running. The drive
# launches bin/kiln for every scenario, so a rebuild mid-run tests a mix of
# two binaries and its results mean nothing.
cmd=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("command",""))')
# Only commands that run: `make build|qa` at the start of a command (not
# the words inside a commit message or a string), and `-o bin/kiln` itself
# (-o bin/kiln-drive or bin/faux are fine).
if ! printf '%s\n' "$cmd" | grep -Eq '(^|[;&|(]|then|do)[[:space:]]*make([[:space:]]+-[^[:space:]]+)*[[:space:]]+(build|qa)([[:space:]]|$|;|&)|-o (\./)?bin/kiln( |$|;|&)'; then
  exit 0
fi
# A Python process running the driver, not any process whose command line
# merely mentions its path (a grep, or the shell running this very call).
if pgrep -f '^[^ ]*[Pp]ython[0-9.]* ([^ ]+ )*scripts/qa/drive\.py' >/dev/null 2>&1; then
  echo "A real-terminal drive (scripts/qa/drive.py) is running and uses bin/kiln. Wait for it to finish before rebuilding or starting another drive." >&2
  exit 2
fi
exit 0
