#!/bin/bash
cat >/dev/null
jq -n '{hookSpecificOutput:{additionalContext:"extra context from hook"}}'
