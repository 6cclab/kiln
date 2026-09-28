#!/bin/sh
# PreToolUse(Bash): refuse AppleScript `write text` aimed at a terminal.
# Injected bytes skip the terminal's own key encoder, so a drive that uses
# them tests something no user can do. Input to a running kiln goes through
# Orca (scripts/qa/drive.py TYPE/KEY/SEND).
cmd=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("command",""))')
case "$cmd" in
  *osascript*"write text"*)
    echo "Do not write text into a terminal with AppleScript. Drive kiln through Orca: scripts/qa/drive.py TYPE, KEY or SEND steps." >&2
    exit 2 ;;
esac
exit 0
