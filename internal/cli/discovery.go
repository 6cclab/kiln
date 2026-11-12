package cli

import (
	"context"
	"strings"

	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/skills"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tools"
)

const discoveryPointer = "Installed skills and MCP tools are available through capability_search. Search by topic when specialized instructions or tools would help, then invoke a skill by exact name or call an enabled MCP tool. Relevant suggestions may accompany the user request.\n"

const legacyDiscoveryPointer = "Installed skills are available through skill_search. Search by topic when specialized instructions would help, then invoke skill by exact name. Relevant suggestions may accompany the user request.\n"

func discoveryRecords(records []tools.SkillRecord, config claudesettings.SkillDiscovery) []tools.SkillRecord {
	excluded := map[string]bool{}
	for _, name := range config.Excluded {
		excluded[name] = true
	}
	out := make([]tools.SkillRecord, 0, len(records))
	for _, r := range records {
		if !excluded[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

func discoveryIndex(list []skills.Skill, config claudesettings.SkillDiscovery, tier int) string {
	if config.Mode != "catalog" {
		if len(list) == 0 {
			return ""
		}
		if config.Unified != nil && !*config.Unified {
			return legacyDiscoveryPointer
		}
		return discoveryPointer
	}
	var selected []skills.Skill
	for _, s := range list {
		if s.DisableModelInvocation {
			continue
		}
		excluded := false
		for _, name := range config.Excluded {
			if s.Name == name {
				excluded = true
			}
		}
		if !excluded {
			selected = append(selected, s)
		}
	}
	return formatSkillsIndex(selected, config.Budget(tier))
}

// Suggestions are derived from the latest user request, not tool output or
// other runtime context. Append them without rewriting the conversation.
func skillDiscoveryContext(records []tools.SkillRecord, config claudesettings.SkillDiscovery, budget func() int) func(context.Context, []msg.Message) ([]msg.Message, error) {
	return func(_ context.Context, transcript []msg.Message) ([]msg.Message, error) {
		if config.Mode == "search" || config.Mode == "catalog" {
			return transcript, nil
		}
		for i := len(transcript) - 1; i >= 0; i-- {
			user, ok := transcript[i].(msg.UserMessage)
			if !ok || user.KilnHook != "" {
				continue
			}
			query := user.KilnTyped
			if query == "" {
				query = msg.TextOf(user.Content)
			}
			if strings.TrimSpace(query) == "" {
				return transcript, nil
			}
			// Pins precede ranked suggestions but share the same hard byte budget.
			var pinned []tools.SkillRecord
			rest := append([]tools.SkillRecord(nil), records...)
			for _, name := range config.Pinned {
				for _, r := range records {
					if r.Name == name {
						pinned = append(pinned, r)
						break
					}
				}
			}
			const prefix = "<skill-suggestions>\n"
			const suffix = "Use skill to load instructions; skill_search finds other capabilities.\n</skill-suggestions>"
			text := ""
			remaining := budget() - (len(prefix)+len(suffix)+3)/4
			for _, r := range pinned {
				line := tools.SkillSearchText([]tools.SkillRecord{r}, "", remaining)
				text += line
				remaining -= (len(line) + 3) / 4
				for j := 0; j < len(rest); j++ {
					if rest[j].Name == r.Name {
						rest = append(rest[:j], rest[j+1:]...)
						break
					}
				}
			}
			text += tools.SkillSearchText(rest, query, remaining)
			if text == "" {
				return transcript, nil
			}
			return appendRuntimeContext(transcript, prefix+text+suffix), nil
		}
		return transcript, nil
	}
}
