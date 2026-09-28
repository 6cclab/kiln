#!/bin/sh
# PreToolUse(Bash): refuse `UPDATE=1 go test` without -run. A suite-wide
# update rewrites every golden, including ones captured mid-render, and the
# diff that should have been reviewed disappears into the noise.
cmd=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("command",""))')
case "$cmd" in
  *UPDATE=1*"go test"*) ;;
  *) exit 0 ;;
esac
case "$cmd" in
  *" -run "*|*" -run="*) exit 0 ;;
esac
echo "UPDATE=1 rewrites goldens: limit it to the tests you changed with -run '<TestName>|<TestName>', then review git diff testdata before committing." >&2
exit 2
