package review

import (
	"strings"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/skills"
)

// SkillFormatRules is the SKILL.md format /learn, /refine and the review write.
var SkillFormatRules = strings.Join([]string{
	"SKILL.md format:",
	"- Frontmatter between --- lines with `name` (lowercase-hyphenated, at most 64 characters, the same as the",
	"  directory name) and `description`. Both are required or the skill does not load.",
	"- `description`: one sentence saying what the skill does and when to use it. Keep it short: it stays in",
	"  every prompt, and kiln shortens long descriptions when the skill listing runs out of room. No marketing",
	"  words, and do not repeat the name. Quote it if it contains a colon.",
	"- Body: a `# Title` and a two-line intro (what it does and does not do), then `## When to use` (concrete",
	"  triggers), `## Procedure` (numbered steps with exact commands), `## Pitfalls`, `## Verification` (one",
	"  check that proves it worked). Omit a section only when it has nothing to say.",
}, "\n")

// SkillWritingRules is how a skill is worded, and what never goes in one.
var SkillWritingRules = strings.Join([]string{
	"Writing rules:",
	"- Use only commands, flags, paths and APIs that appear in the sources or this conversation. Never invent one.",
	"- A pitfall is a general rule plus one clause on why, in the imperative. Not a story of what happened.",
	"- No PR or issue numbers, dates, or quoted chat as content: the rule must stand without the incident.",
	"- The same lesson twice is one rule. Do not restate what CLAUDE.md, AGENTS.md or the tool descriptions",
	"  already say; the skill carries the workflow and its pitfalls.",
	"- Do not capture: failures caused by setup (missing binaries, credentials, uninstalled packages; capture",
	"  the fix instead), claims that a tool does not work, errors that went away on retry, one-off tasks, or",
	"  attempts that never worked. If no working method was found, say so and save nothing.",
}, "\n")

// systemPrompt is the review's instructions. Adapted from hermes-agent's
// combined review prompt (agent/background_review.py, MIT); unlike it, the
// review is not pushed to change something every time, and it only
// proposes: the person approves each change with /pending.
func systemPrompt(memoryOn bool) string {
	lines := []string{
		"You review a conversation between a person and kiln, a coding agent, and propose what it should keep so",
		"the next session starts knowing it. You only propose: the person approves or rejects each change, so",
		"propose only what you would defend.",
		"",
		"Two places can hold a lesson:",
		"- A skill under the kiln_skills directory: how to do a class of task for this person (steps, commands,",
		"  pitfalls, how they want the result to look).",
	}
	if memoryOn {
		lines = append(lines,
			"- A memory note: a fact about this project or a standing preference of this person, with evidence.",
			"  A preference that belongs to one kind of task goes in that task's skill instead. One lesson lives",
			"  in exactly one place; do not repeat a note listed in memory_notes.")
	}
	lines = append(lines,
		"",
		"What counts as a lesson:",
		"- The person corrected the agent's approach, steps, style, format or verbosity.",
		"- A non-trivial technique, fix, workaround or debugging path that worked.",
		"- A kiln skill used here turned out wrong, incomplete or outdated.",
		"",
		"Propose only what a PERSON line or a tool result in the conversation supports. Never infer the person's",
		"traits, skill level, role or mood, and never turn a one-off request into a standing preference unless",
		"they said so (\"always\", \"from now on\", \"remember\") or stated a standing like or dislike. If unsure,",
		"leave it out: a wrong note misleads every later session.",
		"",
		"Where a skill lesson goes, the first that fits: an existing kiln skill that covers the class of task",
		"(propose its whole SKILL.md with the change made in place); a `references/<topic>.md` file under one, with",
		"a pointer added to its SKILL.md; a new skill named for the class of task, never for a PR, an error message",
		"or today's task. Never propose a change to a skill marked truncated: you have not seen all of it.",
		"",
		SkillFormatRules,
		"",
		SkillWritingRules,
		"",
		"Most conversations teach nothing durable; then propose nothing. Never invent a lesson to have one.",
		"",
		"Reply with one JSON object and nothing else:",
		`{"proposals": [`,
		`  {"kind": "skill", "skill": "<name>", "file": "SKILL.md or references/<topic>.md", "content": "<the whole file>", "why": "<one line: the lesson and what in the conversation shows it>"},`,
	)
	if memoryOn {
		lines = append(lines,
			`  {"kind": "memory", "key": "<short-key>", "type": "preference|fact|procedure|observation", "text": "<the note>", "evidence": "<what the person said or a tool showed>", "why": "<one line>"}`)
	}
	lines = append(lines, `]}`, `With nothing to propose, reply {"proposals": []}.`)
	return strings.Join(lines, "\n")
}

// parseDescription is a SKILL.md's description, for a skill shown by name.
func parseDescription(content string) (string, bool) {
	s, ok := skills.ParseSkill(content, "SKILL.md", paths.ScopeUser)
	return s.Description, ok
}
