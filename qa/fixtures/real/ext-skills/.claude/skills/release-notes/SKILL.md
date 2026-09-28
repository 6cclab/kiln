---
name: release-notes
description: Draft release notes from recent commits
user-invocable: true
---
Run `git log -5 --oneline` in this project. Turn the output into release
notes with a "## Unreleased" heading and one bullet per commit, each
starting with a past-tense verb. Print the notes; do not write a file.
