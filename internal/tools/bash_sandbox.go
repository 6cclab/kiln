package tools

import (
	"encoding/json"
	"strings"

	"github.com/andrepato/harness/internal/execenv"
)

// The bash tool under an OS sandbox (internal/sandbox): the command is
// wrapped by env.Sandbox, a failure the sandbox caused gets a note naming
// what was refused, and — when sandbox.allowUnsandboxedCommands allows it —
// the model may ask to run a command outside the sandbox with
// dangerouslyDisableSandbox, which the permission gate then asks the user
// about (Claude Code's "unsandboxed retry escape hatch").

var bashParametersUnsandboxable = json.RawMessage(`{
	"type": "object",
	"properties": {
		"command": {"type": "string", "description": "Bash command to execute"},
		"timeout": {"type": "number", "description": "Timeout in seconds (optional, default 120, max 600)"},
		"dangerouslyDisableSandbox": {"type": "boolean", "description": "Run this command outside the OS sandbox. Only after it failed because of the sandbox; the user is asked to approve it."}
	},
	"required": ["command"]
}`)

// bashParametersFor is the bash schema: dangerouslyDisableSandbox is
// offered only while a sandbox runs and the unsandboxed retry is allowed.
func bashParametersFor(env *execenv.Env) json.RawMessage {
	if env != nil && env.Sandbox != nil && env.Sandbox.OffersUnsandboxed() {
		return bashParametersUnsandboxable
	}
	return bashParameters
}

// sandboxDescription is what the bash description adds while commands
// run sandboxed.
func sandboxDescription(env *execenv.Env) string {
	if env == nil || env.Sandbox == nil || !env.Sandbox.Active() {
		return ""
	}
	s := " Commands run inside an OS sandbox: they can write only to the working directory, added directories and $TMPDIR, " +
		"and reach the network only through a proxy that allows approved hosts."
	if env.Sandbox.OffersUnsandboxed() {
		s += " If a command fails because the sandbox refused something, you may run it again with dangerouslyDisableSandbox: true; " +
			"the user is asked to approve running it outside the sandbox. Do not use it otherwise."
	}
	return s
}

// commandSandbox is how one command runs: inside the sandbox, or nil.
func commandSandbox(env *execenv.Env, command string, disable bool) execenv.CommandSandbox {
	if env == nil || env.Sandbox == nil {
		return nil
	}
	return env.Sandbox.ForCommand(command, disable)
}

// miscasedKey returns an argument name that differs from one of names only
// in case ("Command", "DangerouslyDisableSandbox"), or "". encoding/json
// would decode it into the field, while the permission gate reads the
// arguments by their exact names: the two must never judge different
// commands, or a different sandbox choice.
func miscasedKey(raw json.RawMessage, names ...string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for k := range m {
		for _, n := range names {
			if k != n && strings.EqualFold(k, n) {
				return k
			}
		}
	}
	return ""
}
