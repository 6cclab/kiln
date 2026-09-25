#!/bin/bash
# PreToolUse hook for test/e2e/mcp_behaviour_test.go's
# TestMCP_PreToolUseRewriteReachesFixture. Mirrors
# testdata/hooks/rewrite-updated-input.sh's pattern (ignore the hook's
# stdin payload, unconditionally rewrite the tool's args) but for the
# fixture's echo tool: it replaces whatever "text" the model sent with
# "rewritten-by-hook", so the test can prove the fixture (an
# out-of-process stdio server, not just the harness's in-memory intent)
# actually received the rewritten value by checking the echoed content
# that round-trips back into the next provider request.
cat >/dev/null
jq -n '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"allow",updatedInput:{text:"rewritten-by-hook"}}}'
