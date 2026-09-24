// Package writesettings ports harness/src/claude/write-settings.ts:
// read-modify-write of <cwd>/.claude/settings.local.json only — never
// settings.json, which is the project's shared, usually-git-tracked
// policy. AddRule/RemoveRule preserve every other key in the file,
// dedup on add, create the .claude directory if needed, and write with
// 2-space indentation and a trailing newline.
package writesettings
