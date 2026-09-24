// Package settings ports harness/src/claude/settings.ts: loading and
// merging .claude/settings.json across scopes (permission lists
// concatenate, scalars use last-non-empty-wins, env shallow-merges),
// matching a tool call against one permission rule string (MatchesRule),
// and deciding whether a call may proceed given the merged rules and the
// active PermissionMode (Decide).
package settings
