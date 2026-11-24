package commands

import (
	"context"
	"strings"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/review"
)

// refinePrompt is the prompt /refine sends: review this conversation and
// save what it taught, as skill updates under skillsDir (~/.kiln/skills)
// and memory notes (memory_write). It runs in the session, so every write
// goes through the permission gate: skill files are on a protected path,
// and memory_write asks outside auto and bypassPermissions.
//
// Adapted from hermes-agent's background review prompts
// (agent/background_review.py, NousResearch, MIT): the grounding rule, the
// order of where a lesson goes, and the one-place rule for preferences.
// Their prompt pushes the reviewer to change something every time; this
// one does not, since kiln only keeps what the conversation supports.
func refinePrompt(focus, skillsDir string) string {
	lines := []string{
		"[/refine] Review the conversation above and save what it taught, so the next session starts knowing it.",
		"Two places can hold a lesson:",
		"- Skills, under " + skillsDir + ": how to do a class of task for this person (steps, commands, pitfalls,",
		"  how they want the result to look).",
		"- Memory, with `memory_write`: facts about this project and this person's standing preferences, with",
		"  evidence. A preference that belongs to one kind of task goes in that task's skill instead; one lesson",
		"  lives in exactly one place, never both.",
		"",
		"What counts as a lesson:",
		"- The person corrected your approach, steps, style, format or verbosity (\"stop doing X\", \"too long\",",
		"  \"don't format like this\"). Put the correction in the skill that governs that task.",
		"- A non-trivial technique, fix, workaround or debugging path that worked.",
		"- A skill used in this conversation turned out wrong, incomplete or outdated.",
		"",
		"Save only what you can point to in this conversation: a message from the person or a tool result. Never",
		"infer their traits, skill level, role or mood from the topic, tone or tools, and never turn a one-off",
		"request into a standing preference unless they said so (\"always\", \"from now on\", \"remember\") or stated a",
		"standing like or dislike. If you are unsure, leave it out: a wrong note misleads every later session.",
		"",
		"Where a skill lesson goes, the first that fits:",
		"1. A skill under " + skillsDir + " used in this conversation, if it covers the lesson.",
		"2. Another skill under " + skillsDir + " that covers this class of task (list the folder to find it).",
		"3. A `references/<topic>.md` file under such a skill, for depth only needed sometimes, with a one-line",
		"   pointer in its SKILL.md. Extend an existing topic file before adding one; never one file per session.",
		"4. A new skill, named for the class of task. Never name it after a PR, an error message or today's task.",
		"Change skills only under " + skillsDir + ". A .claude skill is the person's own and kiln does not edit",
		"it: if one was wrong, write the fix into a kiln skill, or tell the person what to change in it.",
		"Read a SKILL.md in full right before editing it, and fix a wrong sentence in place instead of adding a",
		"correction below it.",
		"",
		review.SkillFormatRules,
		"",
		review.SkillWritingRules,
		"",
		"Saying nothing needs saving is a fine outcome when the conversation taught nothing durable. Do not invent",
		"a lesson to have something to save.",
		"",
		"When done, list each change in one line (skill or memory key, and what it now says), any change the",
		"person declined, and any two skills that overlap. Saved skills load from the next kiln session.",
	}
	if focus = strings.TrimSpace(focus); focus != "" {
		lines = append(lines, "", "The person asked you to focus on this; it comes first:", focus)
	}
	return strings.Join(lines, "\n")
}

// refineCommand is /refine.
func refineCommand() Command {
	return Command{
		Name:         "refine",
		Description:  "Save what this conversation taught as skill updates and memory notes",
		ArgumentHint: "[focus]",
		Run: func(_ context.Context, args string) (Result, error) {
			return Result{Prompt: refinePrompt(args, paths.KilnSkillsDir())}, nil
		},
	}
}
