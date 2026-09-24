package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/claude/paths"
	"gopkg.in/yaml.v3"
)

// Origin distinguishes a personal (user-scope) command from a project one.
type Origin string

const (
	Personal Origin = "personal"
	Project  Origin = "project"
)

// Frontmatter is the parsed YAML header of a command file.
type Frontmatter struct {
	Description  string
	ArgumentHint string
	AllowedTools []string
	Model        string
}

type rawFrontmatter struct {
	Description  string `yaml:"description"`
	ArgumentHint string `yaml:"argument-hint"`
	AllowedTools any    `yaml:"allowed-tools"`
	Model        string `yaml:"model"`
}

var frontmatterRe = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---\r?\n?(.*)$`)

func parseAllowedTools(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		var out []string
		for _, t := range strings.Split(x, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				out = append(out, t)
			}
		}
		return out
	case []any:
		var out []string
		for _, item := range x {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

// SplitFrontmatter splits `---\n...\n---\n<body>`. Returns the whole
// input as body when no frontmatter block is present. Malformed YAML
// keeps the body usable rather than dropping the command: a command that
// runs without its description beats one that vanishes.
func SplitFrontmatter(source string) (Frontmatter, string) {
	m := frontmatterRe.FindStringSubmatch(source)
	if m == nil {
		return Frontmatter{}, source
	}
	var raw rawFrontmatter
	if err := yaml.Unmarshal([]byte(m[1]), &raw); err != nil {
		return Frontmatter{}, m[2]
	}
	return Frontmatter{
		Description:  raw.Description,
		ArgumentHint: raw.ArgumentHint,
		AllowedTools: parseAllowedTools(raw.AllowedTools),
		Model:        raw.Model,
	}, m[2]
}

var (
	argumentsRe       = regexp.MustCompile(`\$ARGUMENTS`)
	usesPlaceholderRe = regexp.MustCompile(`\$ARGUMENTS|\$\d`)
	positionalRe      = regexp.MustCompile(`\$(\d+)`)
)

// ApplyArguments substitutes arguments into a command body.
//
// $ARGUMENTS takes everything; $1, $2, ... take positional words. A
// template using neither placeholder is left alone and the arguments are
// appended instead, so a bare prompt template still receives what the
// user typed rather than silently discarding it.
func ApplyArguments(body, args string) string {
	positional := strings.Fields(args)
	usesPlaceholders := usesPlaceholderRe.MatchString(body)

	out := argumentsRe.ReplaceAllString(body, args)
	out = positionalRe.ReplaceAllStringFunc(out, func(m string) string {
		n, _ := strconv.Atoi(m[1:])
		if n-1 >= 0 && n-1 < len(positional) {
			return positional[n-1]
		}
		return ""
	})

	if !usesPlaceholders && args != "" {
		out = out + "\n\n" + args
	}
	return out
}

// CommandFile is a neutral representation of a .claude/commands/*.md
// file, exposed for a future command registry (phase 5) to consume.
type CommandFile struct {
	Name         string
	Namespace    string // "" when the command has no namespace.
	Description  string
	ArgumentHint string
	Origin       Origin
	Path         string
	body         string
}

// Render applies args to the command's body, exactly as ApplyArguments
// does.
func (c CommandFile) Render(args string) string {
	return ApplyArguments(strings.TrimSpace(c.body), args)
}

func walkMarkdown(dir string) []string {
	var found []string
	var walk func(string)
	walk = func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			// A missing commands directory is the normal case, not an
			// error.
			return
		}
		for _, entry := range entries {
			full := filepath.Join(d, entry.Name())
			if entry.IsDir() {
				walk(full)
			} else if strings.HasSuffix(entry.Name(), ".md") {
				found = append(found, full)
			}
		}
	}
	walk(dir)
	return found
}

func loadFrom(dir string, origin Origin) []CommandFile {
	var out []CommandFile
	for _, file := range walkMarkdown(dir) {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		fm, body := SplitFrontmatter(string(data))

		// commands/frontend/component.md -> namespace "frontend", name "component".
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			rel = file
		}
		rel = strings.TrimSuffix(rel, ".md")
		segments := strings.Split(rel, string(filepath.Separator))
		name := segments[len(segments)-1]
		namespace := ""
		if len(segments) > 1 {
			namespace = strings.Join(segments[:len(segments)-1], ":")
		}

		out = append(out, CommandFile{
			Name:         name,
			Namespace:    namespace,
			Description:  fm.Description,
			ArgumentHint: fm.ArgumentHint,
			Origin:       origin,
			Path:         file,
			body:         body,
		})
	}
	return out
}

// LoadCommands loads every .claude/commands/*.md file across scopes, one
// slice per scope in precedence order (personal, then project); the
// registry (phase 5) resolves same-name collisions by registration order.
func LoadCommands(cwd string) []CommandFile {
	var out []CommandFile
	for _, root := range paths.ClaudeRoots(cwd) {
		origin := Project
		if root.Scope == paths.ScopeUser {
			origin = Personal
		}
		out = append(out, loadFrom(filepath.Join(root.Dir, "commands"), origin)...)
	}
	return out
}
