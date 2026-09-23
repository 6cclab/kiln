#!/bin/sh
# context-plaintext.sh
#
# Behaviour: reads (and discards) the hook JSON payload on stdin, then
# writes plain text to stdout and exits 0. Under Claude Code hook
# conventions, a UserPromptSubmit or SessionStart hook's plain stdout (no
# hookSpecificOutput JSON) is added to the model's context verbatim. Used to
# test harness's plaintext-context injection path.
cat >/dev/null
echo "context from hook"
exit 0
