package commands

import (
	"context"

	claudecommands "github.com/andrepato/harness/internal/claude/commands"
	"github.com/andrepato/harness/internal/claude/skills"
)

// SkillSource wraps every user-invocable skill as a command, matching
// cli.ts's inline skillCommandSource: only skills with `user-invocable:
// true` in their frontmatter are offered (a skill Claude decides to use
// on its own is not something the palette should also list), the argument
// hint says the whole point of the argument ("further instructions"), and
// running one sends its body plus whatever the user typed, joined by a
// blank line so the two are visually distinct in the prompt.
func SkillSource(list []skills.Skill) Source {
	return Source{
		Origin: OriginSkill,
		Load: func() ([]Command, error) {
			var out []Command
			for _, s := range list {
				if !s.UserInvocable {
					continue
				}
				s := s
				out = append(out, Command{
					Name:         s.Name,
					Description:  s.Description,
					ArgumentHint: "[instructions]",
					Origin:       OriginSkill,
					Run: func(ctx context.Context, args string) (Result, error) {
						prompt := s.Content
						if args != "" {
							prompt = s.Content + "\n\n" + args
						}
						return Result{Prompt: prompt}, nil
					},
				})
			}
			return out, nil
		},
	}
}

// ClaudeCommandSources returns one Source per scope for .claude/commands/
// *.md files: personal (~/.claude/commands), then project (<cwd>/.claude/
// commands). Registered in that order, a project command of the same name
// shadows a personal one, matching how the settings hierarchy resolves.
func ClaudeCommandSources(cwd string) []Source {
	load := func(origin claudecommands.Origin, asOrigin Origin) Source {
		return Source{
			Origin: asOrigin,
			Load: func() ([]Command, error) {
				var out []Command
				for _, f := range claudecommands.LoadCommands(cwd) {
					if f.Origin != origin {
						continue
					}
					f := f
					out = append(out, Command{
						Name:         f.Name,
						Namespace:    f.Namespace,
						Description:  f.Description,
						ArgumentHint: f.ArgumentHint,
						Origin:       asOrigin,
						Run: func(ctx context.Context, args string) (Result, error) {
							return Result{Prompt: f.Render(args)}, nil
						},
					})
				}
				return out, nil
			},
		}
	}
	return []Source{
		load(claudecommands.Personal, OriginPersonal),
		load(claudecommands.Project, OriginProject),
	}
}
