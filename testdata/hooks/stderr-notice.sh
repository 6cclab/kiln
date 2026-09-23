#!/bin/sh
# stderr-notice.sh
#
# Behaviour: reads (and discards) the hook JSON payload on stdin, writes a
# notice to stderr, and exits 0. Under Claude Code hook conventions, a
# non-blocking exit code (0) with stderr output is a notice: it is
# typically surfaced to the user/transcript but does not deny the action or
# get added to the model's context the way stdout does. Used to test that
# harness distinguishes stderr-on-success from stderr-on-block (exit 2).
cat >/dev/null
echo "notice from hook" >&2
exit 0
