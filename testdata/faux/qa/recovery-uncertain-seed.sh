#!/bin/sh
# Seed a crash after a visible tool effect, before kiln can save its outcome.
set -eu

root=$1
project=$2
home=$3
faux_log=$project/recovery-faux.log
kiln_log=$project/recovery-kiln.log
"$root/bin/faux" "$root/testdata/faux/qa/recovery-uncertain-seed.yaml" > "$faux_log" 2>&1 &
faux_pid=$!
kiln_pid=
cleanup() {
  if [ -n "$kiln_pid" ]; then kill "$kiln_pid" 2>/dev/null || :; fi
  touch "$project/release-tool"
  kill "$faux_pid" 2>/dev/null || :
  wait "$faux_pid" 2>/dev/null || :
}
trap cleanup EXIT HUP INT TERM

attempt=0
while [ ! -s "$faux_log" ]; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 100 ] || { echo 'faux did not start' >&2; exit 1; }
  sleep 0.1
done
faux_addr=$(sed -n '1p' "$faux_log")
(
  cd "$project"
  HOME="$home" HARNESS_OFFLINE=1 HARNESS_FAUX_ADDR="$faux_addr" HARNESS_FAUX_API=anthropic-messages \
    exec "$root/bin/kiln" --model faux/faux-1 -p 'run the marker command' \
    --permission-mode bypassPermissions --output-format text
) > "$kiln_log" 2>&1 &
kiln_pid=$!

attempt=0
while :; do
  if [ -f "$project/resume-effect.txt" ] &&
      grep -Rq '"namespace":"pi.op.tool_args"' "$home/.harness/sessions" 2>/dev/null; then
    break
  fi
  kill -0 "$kiln_pid" 2>/dev/null || { cat "$kiln_log" >&2; echo 'kiln exited before marker effect' >&2; exit 1; }
  attempt=$((attempt + 1))
  [ "$attempt" -lt 100 ] || { cat "$kiln_log" >&2; echo 'tool marker timed out' >&2; exit 1; }
  sleep 0.1
done
kill -KILL "$kiln_pid"
wait "$kiln_pid" 2>/dev/null || :
kiln_pid=
touch "$project/release-tool"
[ "$(cat "$project/resume-effect.txt")" = x ]
! grep -Rq '"at":"outcome_ready"' "$home/.harness/sessions"
