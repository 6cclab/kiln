// Package hooks ports harness/src/claude/{hooks,hook-runner}.ts: loading
// and matching .claude/settings.json hook configuration (hooks.go) and
// running hook commands as child processes with a JSON payload on stdin,
// interpreting their exit code and stdout, and composing PreToolUse hooks
// with the permission gate (runner.go).
//
// A hook can observe, inject context, rewrite a tool's arguments before it
// runs, or refuse the call outright. GuardToolCall runs hooks strictly
// before the permission gate, because a hook may rewrite the command and
// the gate must judge what will actually execute.
package hooks
