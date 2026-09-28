#!/usr/bin/env python3
"""UserPromptSubmit: appends a house-rule note to every prompt's context."""
import json

print(json.dumps({
    "hookSpecificOutput": {
        "hookEventName": "UserPromptSubmit",
        "additionalContext": "House rule: never touch migrations/ directly.",
    }
}))
