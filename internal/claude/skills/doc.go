// Package skills loads .claude/skills/*/SKILL.md definitions: YAML
// frontmatter (name, description, user-invocable) plus a body kept for
// injection on invocation, across the user/project scopes with a later
// scope winning by name.
//
// This is a from-scratch Go implementation rather than a port of
// harness/src/claude/skills.ts, which delegates parsing and directory
// walking to pi-agent-core's loadSourcedSkills (no Go equivalent exists).
// The behaviour it implements — frontmatter shape, default
// user-invocable:true, later-scope-wins — matches the brief for this
// package directly.
package skills
