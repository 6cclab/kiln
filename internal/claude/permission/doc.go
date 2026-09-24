// Package permission ports harness/src/claude/permission.ts: the gate that
// decides whether a tool call may proceed, given the merged settings
// permission rules, the active PermissionMode, a workspace boundary, and
// (for anything ask/allow-outside-workspace) a user Prompter.
//
// A denial from Check is not a Go error: it is a *BlockResult carrying a
// reason written for the model, so a refused call ends the tool call
// without ending the session.
package permission
