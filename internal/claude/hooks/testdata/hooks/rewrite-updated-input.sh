#!/bin/bash
cat >/dev/null
jq -n '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"allow",updatedInput:{command:"rtk git status"}}}'
