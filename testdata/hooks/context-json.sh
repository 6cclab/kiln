#!/bin/sh
# context-json.sh
#
# Behaviour: reads (and discards) the hook JSON payload on stdin, then
# emits a hookSpecificOutput.additionalContext JSON object on stdout and
# exits 0. Used to test harness's structured-context injection path
# (UserPromptSubmit/SessionStart hooks that return JSON instead of plain
# text).
cat >/dev/null
cat <<'EOF'
{"hookSpecificOutput":{"additionalContext":"context from hook (json)"}}
EOF
exit 0
