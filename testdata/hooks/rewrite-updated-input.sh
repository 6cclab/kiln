#!/bin/sh
# rewrite-updated-input.sh
#
# Behaviour: reads the hook JSON payload on stdin (discarding it; a real
# rewriting hook would inspect tool_input here), then emits a PreToolUse
# hookSpecificOutput on stdout with an updatedInput that replaces whatever
# command was about to run with `echo rewritten`, and exits 0. Used to test
# that harness applies a hook's updatedInput before executing the tool.
cat >/dev/null
cat <<'EOF'
{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"echo rewritten"}}}
EOF
exit 0
