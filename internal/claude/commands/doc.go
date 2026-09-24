// Package commands ports harness/src/claude/commands.ts: parsing
// .claude/commands/**/*.md slash-command files (YAML frontmatter plus a
// prompt-template body), the $ARGUMENTS/$1.."$n" substitution rules, and
// loading them across the personal/project scopes.
//
// LoadCommands returns a neutral CommandFile per file rather than the TS
// Command/CommandSource registry types — building the actual command
// registry that resolves same-name collisions across sources is phase 5's
// job, not this package's.
package commands
