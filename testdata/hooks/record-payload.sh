#!/bin/sh
# record-payload.sh
#
# Behaviour: appends the hook JSON payload received on stdin to the file
# named by the HOOK_RECORD environment variable, then exits 0. Used by
# tests to capture exactly what harness sends a hook (event name, tool
# name, tool input, session id, etc.) and assert on it after the run.
#
# Requires: HOOK_RECORD set to a writable file path.
if [ -z "$HOOK_RECORD" ]; then
	echo "record-payload.sh: HOOK_RECORD is not set" >&2
	exit 1
fi
cat >>"$HOOK_RECORD"
echo >>"$HOOK_RECORD"
exit 0
