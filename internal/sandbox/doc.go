// Package sandbox runs the bash tools' commands inside an OS sandbox, the
// way Claude Code's sandboxed Bash tool does
// (code.claude.com/docs/en/sandboxing), configured by the "sandbox" key of
// settings.json (internal/claude/settings/sandbox.go).
//
// A Manager is one session's sandbox. It decides which commands run
// sandboxed (sandbox.enabled, excludedCommands, the model's
// dangerouslyDisableSandbox request when allowUnsandboxedCommands
// permits it), builds each command's Plan from the configuration and the
// current workspace roots, and wraps the command for the platform:
//
//   - macOS: /usr/bin/sandbox-exec with a generated Seatbelt profile
//     (seatbelt.go);
//   - Linux: bubblewrap with read-only and read-write binds and a private
//     network namespace, relayed to the proxy through socat (bwrap.go);
//   - elsewhere: unsupported, reported by Unavailable.
//
// Network access goes through Proxy, a local HTTP/CONNECT proxy that
// enforces the domain allow and deny lists and asks the permission layer
// about other hosts. The permission gate (internal/claude/permission)
// auto-allows sandboxed commands when autoAllowBashIfSandboxed is on.
//
// What the sandbox does not cover, as in Claude Code: the file tools
// (read, edit, write), hooks, MCP servers, the status line command, and
// commands the user types at the "!" prompt.
package sandbox
