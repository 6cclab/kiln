#!/bin/sh
# torn-session.sh <root> <faux script>: in the scenario's project (cwd) and
# scratch HOME, run one `kiln -p` turn against its own faux server, then
# append half a transaction line with no newline to that session file, the
# state a kill mid-append leaves behind. Run from an @setup directive: the
# driver's own faux server and @pre runs start after @setup, too late to
# tear the file before launch.
set -e
root=$1
log=$(mktemp)
"$root/bin/faux" "$root/$2" >"$log" 2>&1 &
fp=$!
trap 'kill $fp 2>/dev/null; rm -f "$log"' EXIT
addr=
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
  addr=$(head -n 1 "$log")
  [ -n "$addr" ] && break
  sleep 0.1
done
[ -n "$addr" ] || { echo "faux did not report an address" >&2; exit 1; }
HARNESS_OFFLINE=1 HARNESS_FAUX_ADDR=$addr HARNESS_FAUX_API=anthropic-messages PWD=$(pwd) \
  "$root/bin/kiln" --model faux/faux-1 -p "seed the torn session" >/dev/null
f=$(ls -t "$HOME"/.harness/sessions/*/*.jsonl | head -n 1)
printf '{"seq":9999,"writes":[{"op":"set","addr":"pi.entry/' >>"$f"
echo "$f" >"$HOME/torn-session-path"
