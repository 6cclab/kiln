// Package commands is the Go port of harness/src/commands/{registry,
// builtins,session-commands,manage-commands,inspect-commands,
// account-commands}.ts and the inline /posture, /bashes, /todos commands
// registered in cli.ts.
//
// Design, ported from registry.ts's own comment: in Claude Code, built-ins,
// project commands, plugin commands and skills all resolve in a single flat
// namespace — that is why --disable-slash-commands is documented as
// "Disable all skills". So this package is one Registry with pluggable
// Sources rather than a fixed built-in list that later phases patch onto.
//
// Registration order is precedence, least-specific first: a later source's
// command shadows an earlier one of the same qualified name. The integrator
// (internal/cli) wires sources in the exact order cli.ts does — see
// InlineCommands and each Source constructor's doc comment for where it
// sits in that order.
//
// A command's Result carries both a print-mode Output and, for the four
// "manage" commands (permissions, mcp, agents, config) plus /model with no
// argument, a TUI-neutral ModalSpec. The TUI (phase 6) renders ModalSpec;
// print mode (harness -p "/mcp") falls back to Output, exactly as the TS
// commands do by returning { modal, output } together.
package commands
