#!/bin/sh
# block-exit2.sh
#
# Behaviour: reads (and discards) the hook JSON payload on stdin, writes a
# fixed message to stderr, and exits 2. Under Claude Code hook conventions,
# exit code 2 from a PreToolUse (or other blocking-capable) hook blocks the
# action and feeds stderr back to the model as the reason. Used to test
# harness's handling of a hook that unconditionally denies an action.
cat >/dev/null
echo "blocked by fixture" >&2
exit 2
