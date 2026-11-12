package cli

import (
	"path/filepath"

	claudecommands "github.com/andrepato/harness/internal/claude/commands"
	"github.com/andrepato/harness/internal/tools"
)

// commandSkillRecords merges model-invocable custom commands into the skill
// catalog in slash-registry precedence order. A later command shadows an
// earlier same-named skill or command even when the later command forbids
// model invocation. Plugin commands must follow personal and project files.
func commandSkillRecords(skills []tools.SkillRecord, commands []claudecommands.CommandFile) []tools.SkillRecord {
	type entry struct {
		record  tools.SkillRecord
		allowed bool
	}
	entries := make([]entry, 0, len(skills)+len(commands))
	for _, skill := range skills {
		entries = append(entries, entry{record: skill, allowed: true})
	}
	for _, command := range commands {
		name := command.Name
		if command.Namespace != "" {
			name = command.Namespace + ":" + name
		}
		entries = append(entries, entry{
			record: tools.SkillRecord{
				Name: name, Description: command.Description,
				Dir: filepath.Dir(command.Path), Render: command.Render,
			},
			allowed: !command.DisableModelInvocation,
		})
	}
	last := make(map[string]int, len(entries))
	for i, e := range entries {
		last[e.record.Name] = i
	}
	out := make([]tools.SkillRecord, 0, len(last))
	for i, e := range entries {
		if last[e.record.Name] == i && e.allowed {
			out = append(out, e.record)
		}
	}
	return out
}
