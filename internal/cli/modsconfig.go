package cli

import (
	"context"
	"strings"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	claudeskills "github.com/andrepato/harness/internal/claude/skills"
	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/tools"
)

// The config rows, and the skill expansion, kiln hands to mods ($.config.*
// and skill.prompt; see mods.Services and mods.Engine.SkillPrompt).

// modsConfigRows are kiln's own /config rows: the two settings a session
// holds. Both change only through their own command, so $.config.set denies
// them (mods.ConfigRow.Via).
func modsConfigRows(model func() string, gate *permission.Gate) []mods.ConfigRow {
	var rows []mods.ConfigRow
	if model != nil {
		rows = append(rows, mods.ConfigRow{Key: "model", Label: "Model", Kind: "text", Value: model(), Via: "/model"})
	}
	if gate != nil {
		rows = append(rows, mods.ConfigRow{Key: "permissionMode", Label: "Permission mode", Kind: "choice",
			Value: string(gate.Mode()), Via: "/plan or the permission mode key",
			Options: []string{string(settings.ModeManual), string(settings.ModeAcceptEdits), string(settings.ModeAuto),
				string(settings.ModeDontAsk), string(settings.ModeBypassPermissions), string(settings.ModePlan)}})
	}
	return rows
}

// skillSourceWord is the SkillSource word for the scope a skill loaded from.
func skillSourceWord(scope paths.Scope) string {
	switch scope {
	case paths.ScopeUser:
		return "userSettings"
	case paths.ScopeProject, paths.ScopeLocal:
		return "projectSettings"
	case paths.ScopePlugin:
		return "plugin"
	}
	return ""
}

// skillPromptInput is the skill.prompt input for a loaded skill and the text
// kiln is about to hand the model.
func skillPromptInput(name, text string, fm map[string]any, scope paths.Scope, path string) mods.SkillPromptInput {
	in := mods.SkillPromptInput{Skill: name, Text: text, Frontmatter: fm, Source: skillSourceWord(scope), Path: path}
	if scope == paths.ScopePlugin {
		in.Plugin, _, _ = strings.Cut(name, ":")
	}
	return in
}

// skillSlashRewriter is the same for a /skill command's prompt.
func skillSlashRewriter(rt func() *modsRuntime) func(context.Context, claudeskills.Skill, string) string {
	return func(ctx context.Context, s claudeskills.Skill, text string) string {
		e := rt().get()
		if e == nil {
			return text
		}
		return e.SkillPrompt(ctx, skillPromptInput(s.Name, text, s.Frontmatter, s.Scope, s.FilePath))
	}
}

// skillRewriter is the Skill tool's rewrite: it raises skill.prompt through
// rt's engine (a runtime with no mods leaves the text).
func skillRewriter(rt func() *modsRuntime) func(context.Context, tools.SkillRecord, string) string {
	return func(ctx context.Context, r tools.SkillRecord, text string) string {
		e := rt().get()
		if e == nil {
			return text
		}
		return e.SkillPrompt(ctx, mods.SkillPromptInput{Skill: r.Name, Text: text, Frontmatter: r.Frontmatter,
			Source: r.Source, Plugin: r.Plugin, Path: r.Path})
	}
}
