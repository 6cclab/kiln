#!/usr/bin/env python3
"""UserPromptSubmit: appends a team convention to every prompt's context."""
import json

print(json.dumps({
    "hookSpecificOutput": {
        "hookEventName": "UserPromptSubmit",
        "additionalContext": "Team convention: keep functions small and add a one-line doc comment to anything you change.",
    }
}))
