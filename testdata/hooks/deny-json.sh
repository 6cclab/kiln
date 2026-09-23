#!/bin/sh
# deny-json.sh
#
# Behaviour: reads (and discards) the hook JSON payload on stdin, then
# emits a PreToolUse hookSpecificOutput on stdout with
# permissionDecision "deny" and a reason, and exits 0. Under Claude Code
# hook conventions this is the structured-JSON equivalent of block-exit2.sh:
# instead of a bare exit 2, the hook returns a JSON decision the harness
# must interpret. Used to test harness's handling of a structured deny.
cat >/dev/null
cat <<'EOF'
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"denied by fixture"}}
EOF
exit 0
