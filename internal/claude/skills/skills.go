package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/andrepato/harness/internal/claude/paths"
	"gopkg.in/yaml.v3"
)

// Skill is one parsed .claude/skills/*/SKILL.md definition.
type Skill struct {
	Name        string
	Description string
	// Content is the body kept after the frontmatter, injected on
	// invocation.
	Content       string
	FilePath      string
	UserInvocable bool
	// DisableModelInvocation is SKILL.md's `disable-model-invocation`
	// frontmatter key: true excludes the skill from the model-invocable
	// `skill` tool's catalog (internal/tools.SkillTool). Distinct from
	// UserInvocable, which controls slash-palette visibility instead - the
	// two are independent axes (Claude Code's skill frontmatter has both).
	DisableModelInvocation bool
	Scope                  paths.Scope
}

type frontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	UserInvocable          *bool  `yaml:"user-invocable"`
	DisableModelInvocation *bool  `yaml:"disable-model-invocation"`
}

var frontmatterRe = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---\r?\n?(.*)$`)

// ParseSkill parses one SKILL.md file's content. Exported so
// internal/claude/plugins can parse a plugin's skills with the exact same
// frontmatter rules, rather than duplicating them.
func ParseSkill(source, filePath string, scope paths.Scope) (Skill, bool) {
	return parseSkill(source, filePath, scope)
}

func parseSkill(source, filePath string, scope paths.Scope) (Skill, bool) {
	m := frontmatterRe.FindStringSubmatch(source)
	if m == nil {
		return Skill{}, false
	}
	var data frontmatter
	if err := yaml.Unmarshal([]byte(m[1]), &data); err != nil {
		return Skill{}, false
	}
	name := strings.TrimSpace(data.Name)
	description := strings.TrimSpace(data.Description)
	if name == "" || description == "" {
		return Skill{}, false
	}
	// Default is true: every skill observed in the wild sets it, and a
	// skill that silently fails to appear in the palette is a worse
	// failure than one that appears when it need not.
	userInvocable := true
	if data.UserInvocable != nil {
		userInvocable = *data.UserInvocable
	}
	disableModelInvocation := false
	if data.DisableModelInvocation != nil {
		disableModelInvocation = *data.DisableModelInvocation
	}
	return Skill{
		Name:                   name,
		Description:            description,
		Content:                strings.TrimSpace(m[2]),
		FilePath:               filePath,
		UserInvocable:          userInvocable,
		DisableModelInvocation: disableModelInvocation,
		Scope:                  scope,
	}, true
}

// IsDirEntry reports whether entry, read from parent, is a directory,
// following a symlink to its target the way Claude Code does (skills are
// often linked in from a shared folder). A dangling link is not a directory.
func IsDirEntry(parent string, entry fs.DirEntry) bool {
	if entry.Type()&fs.ModeSymlink == 0 {
		return entry.IsDir()
	}
	info, err := os.Stat(filepath.Join(parent, entry.Name()))
	return err == nil && info.IsDir()
}

func loadFrom(dir string, scope paths.Scope) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No skills directory is the normal case.
		return nil
	}
	var out []Skill
	for _, entry := range entries {
		if !IsDirEntry(dir, entry) {
			continue
		}
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}
		if skill, ok := parseSkill(string(data), skillPath, scope); ok {
			out = append(out, skill)
		}
	}
	return out
}

// LoadSkills loads every skill definition across scopes. A later scope
// wins by name: a project skill shadows a personal one of the same name.
func LoadSkills(cwd string) []Skill {
	byName := map[string]Skill{}
	var order []string
	for _, root := range paths.ClaudeRoots(cwd) {
		for _, skill := range loadFrom(filepath.Join(root.Dir, "skills"), root.Scope) {
			if _, exists := byName[skill.Name]; !exists {
				order = append(order, skill.Name)
			}
			byName[skill.Name] = skill
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}
