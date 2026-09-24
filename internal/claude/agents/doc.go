// Package agents ports harness/src/claude/agents.ts: parsing
// .claude/agents/*.md subagent definitions (YAML frontmatter plus a
// system-prompt body), loading them across the personal/project scopes
// with project shadowing personal, and resolving an agent's requested
// model against the models this harness actually has configured.
package agents
