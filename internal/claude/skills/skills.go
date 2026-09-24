package skills

import (
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
	Scope         paths.Scope
}

type frontmatter struct {
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	UserInvocable *bool  `yaml:"user-invocable"`
}

var frontmatterRe = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---\r?\n?(.*)$`)

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
	return Skill{
		Name:          name,
		Description:   description,
		Content:       strings.TrimSpace(m[2]),
		FilePath:      filePath,
		UserInvocable: userInvocable,
		Scope:         scope,
	}, true
}

func loadFrom(dir string, scope paths.Scope) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No skills directory is the normal case.
		return nil
	}
	var out []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
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
