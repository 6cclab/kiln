#!/bin/bash
# gap-record-payload-append.sh: like record-payload.sh, but APPENDS one
# line of the raw payload per invocation instead of truncating, so a
# single fixed hook command wired once in settings.json can still capture
# every firing of an event that fires more than once per run (e.g.
# SubagentStop, once per dispatched subagent). Used by hooks_gap_test.go's
# TestHooks_SubagentStop_PerDepth.
cat >> "$HARNESS_TEST_PAYLOAD_FILE"
echo >> "$HARNESS_TEST_PAYLOAD_FILE"
