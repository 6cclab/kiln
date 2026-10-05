#!/bin/bash
# A Stop/SubagentStop hook that blocks (exit 2) until stop_hook_active is
# true, then lets the turn end: the loop guard a Stop hook is expected to
# keep. Appends each payload, one per line, to $HARNESS_TEST_PAYLOAD_FILE
# when that is set.
input=$(cat)
if [ -n "$HARNESS_TEST_PAYLOAD_FILE" ]; then
  printf '%s\n' "$input" >> "$HARNESS_TEST_PAYLOAD_FILE"
fi
case "$input" in
  *'"stop_hook_active":true'*) exit 0 ;;
esac
echo "run the tests first" >&2
exit 2
