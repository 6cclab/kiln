#!/bin/bash
cat >/dev/null
jq -n '{hookSpecificOutput:{permissionDecision:"deny",permissionDecisionReason:"policy"}}'
