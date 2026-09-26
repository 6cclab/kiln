#!/usr/bin/env bash
# visual-drive: run kiln in a real iTerm2 window (the user's own profile,
# font and background), drive it from a step file, and screenshot named
# scenes — the check that emulator goldens cannot do.
#
#   scripts/visual-drive.sh --faux testdata/faux/design-session.yaml \
#       --fixture testdata/behaviour/design --steps testdata/drive/design-visual.txt \
#       --out /tmp/shots [--cols 100 --rows 30] [--mode manual] [-- extra kiln args]
#
# Safety: kiln runs with an empty HOME (no ~/.claude settings, no ~/.harness
# sessions or trust entries), HARNESS_OFFLINE=1 (only faux/ollama) and
# --model faux/faux-1, so it can neither spend money nor touch your data.
#
# Step file, one command per line:
#   SEND <text>      type text and press enter
#   TYPE <text>      type text, no enter
#   KEY <k>          press a key: enter, esc, up, down, ctrl-c, ctrl-f, or a char
#   WAIT <seconds>   sleep
#   SHOT <name>      screenshot the window to <out>/<name>.png
set -euo pipefail
COLS=100 ROWS=30 MODE=manual FAUX="" FIXTURE="" STEPS="" OUT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --cols) COLS=$2; shift 2;; --rows) ROWS=$2; shift 2;; --mode) MODE=$2; shift 2;;
    --faux) FAUX=$2; shift 2;; --fixture) FIXTURE=$2; shift 2;; --steps) STEPS=$2; shift 2;;
    --out) OUT=$2; shift 2;; --) shift; break;; *) echo "unknown arg $1" >&2; exit 2;;
  esac
done
[ -n "$FAUX" ] && [ -n "$STEPS" ] && [ -n "$OUT" ] || { echo "need --faux, --steps, --out" >&2; exit 2; }
ROOT=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$OUT"
WORK=$(mktemp -d /tmp/kiln-visual.XXXXXX)
HOMEDIR="$WORK/home"; mkdir -p "$HOMEDIR"
PROJ="$WORK/proj"; if [ -n "$FIXTURE" ]; then cp -R "$FIXTURE" "$PROJ"; else mkdir -p "$PROJ"; fi
# Trust the fixture up front so the trust dialog does not eat the first steps.
mkdir -p "$HOMEDIR/.harness"; printf '["%s"]\n' "$PROJ" > "$HOMEDIR/.harness/trusted.json"
[ -x "$ROOT/bin/kiln" ] && [ -x "$ROOT/bin/faux" ] || (cd "$ROOT" && make build >/dev/null && go build -o bin/faux ./cmd/faux)
"$ROOT/bin/faux" "$FAUX" > "$WORK/faux.log" 2>&1 & FAUXPID=$!
sleep 1; ADDR=$(head -1 "$WORK/faux.log")
TAG="kiln-visual-$$"
TTY=$(osascript <<APPLESCRIPT
tell application "iTerm2"
  set w to (create window with default profile)
  tell current session of w
    set columns to $COLS
    set rows to $ROWS
    set name to "$TAG"
    write text "cd '$PROJ' && printf '\\\\e]1;$TAG\\\\a' && HOME='$HOMEDIR' HARNESS_OFFLINE=1 HARNESS_FAUX_ADDR=$ADDR HARNESS_FAUX_API=anthropic-messages HARNESS_RETRY_JITTER=0 HARNESS_TEST_CLOCK=2026-01-01T12:00:00Z '$ROOT/bin/kiln' --model faux/faux-1 --permission-mode $MODE $*"
    get tty
  end tell
end tell
APPLESCRIPT
)
sleep 2
WID=$(swift "$ROOT/scripts/lib/windowid.swift" "$TAG" 2>/dev/null || true)
[ -n "$WID" ] || WID=$(swift "$ROOT/scripts/lib/windowid.swift" "kiln" 2>/dev/null)
[ -n "$WID" ] || { echo "could not find the iTerm2 window" >&2; kill $FAUXPID; exit 1; }
sess() { osascript -e "tell application \"iTerm2\" to tell current session of current window to $1"; }
while IFS= read -r line || [ -n "$line" ]; do
  cmd=${line%% *}; arg=${line#* }
  case "$cmd" in
    SEND) sess "write text \"$arg\"";;
    TYPE) sess "write text \"$arg\" newline NO";;
    KEY) case "$arg" in
           enter) sess "write text \"\"";;
           esc) sess "write text (ASCII character 27) newline NO";;
           ctrl-c) sess "write text (ASCII character 3) newline NO";;
           ctrl-f) sess "write text (ASCII character 6) newline NO";;
           up) sess "write text (ASCII character 27 & \"[A\") newline NO";;
           down) sess "write text (ASCII character 27 & \"[B\") newline NO";;
           *) sess "write text \"$arg\" newline NO";;
         esac;;
    WAIT) sleep "$arg";;
    SHOT) screencapture -x -o -l "$WID" "$OUT/$arg.png"; echo "shot $OUT/$arg.png";;
    ''|'#'*) ;;
    *) echo "unknown step: $line" >&2;;
  esac
done < "$STEPS"
sess "write text (ASCII character 3) newline NO"; sleep 0.3; sess "write text (ASCII character 3) newline NO"; sleep 0.5
# kiln retitles the window and session, so close by the tty recorded at creation.
osascript >/dev/null 2>&1 <<APPLESCRIPT || true
tell application "iTerm2"
  repeat with w in windows
    if tty of current session of w is "$TTY" then close w
  end repeat
end tell
APPLESCRIPT
kill $FAUXPID 2>/dev/null || true
rm -rf "$WORK"
echo "done: $OUT"
