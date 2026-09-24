package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/claude/paths"
	"gopkg.in/yaml.v3"
)

// Source distinguishes a personal (user-scope) agent from a project one.
type Source string

const (
	Personal Source = "personal"
	Project  Source = "project"
)

// Definition is one parsed .claude/agents/*.md subagent definition.
type Definition struct {
	Name        string
	Description string
	// Prompt is the system prompt: everything after the frontmatter.
	Prompt string
	// Model is the requested model, verbatim from the file. An alias like
	// "sonnet" is a hint, not a requirement - see ResolveAgentModel.
	Model string
	// Tools is the tool allowlist. Nil means "inherit the parent's tools".
	Tools  []string
	Source Source
	Path   string
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Model       string `yaml:"model"`
	Tools       any    `yaml:"tools"`
}

var frontmatterRe = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---\r?\n?(.*)$`)

// parseTools accepts "Read, Glob, Grep" or a YAML list. A bare `tools:`
// with no value parses to nil, which means "absent" (inherit), not an
// empty tool-less list.
func parseTools(value any) []string {
	if value == nil {
		return nil
	}
	var list []string
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			list = append(list, strings.TrimSpace(toString(item)))
		}
	case string:
		for _, item := range strings.Split(v, ",") {
			list = append(list, strings.TrimSpace(item))
		}
	default:
		list = append(list, strings.TrimSpace(toString(v)))
	}
	cleaned := make([]string, 0, len(list))
	for _, t := range list {
		if t != "" {
			cleaned = append(cleaned, t)
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

// ParseAgent parses one agent definition file's content.
//
// name may be omitted from the frontmatter; the filename is what Claude
// Code falls back to. description is required, since an agent with no
// description has nothing for the model to choose it from.
func ParseAgent(source, path string, scope Source) (Definition, bool) {
	m := frontmatterRe.FindStringSubmatch(source)
	if m == nil {
		return Definition{}, false
	}

	var data frontmatter
	if err := yaml.Unmarshal([]byte(m[1]), &data); err != nil {
		// Unlike a command, an agent with no frontmatter has no name to be
		// dispatched by and no description for the model to choose it
		// from. There is nothing usable to salvage.
		return Definition{}, false
	}

	name := strings.TrimSpace(data.Name)
	if name == "" {
		base := filepath.Base(path)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	description := strings.TrimSpace(data.Description)
	if name == "" || description == "" {
		return Definition{}, false
	}

	return Definition{
		Name:        name,
		Description: description,
		Prompt:      strings.TrimSpace(m[2]),
		Model:       strings.TrimSpace(data.Model),
		Tools:       parseTools(data.Tools),
		Source:      scope,
		Path:        path,
	}, true
}

func loadFrom(dir string, scope Source) []Definition {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No agents directory is the common case, not an error.
		return nil
	}

	var out []Definition
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			// One unreadable file must not cost the others.
			continue
		}
		if agent, ok := ParseAgent(string(data), path, scope); ok {
			out = append(out, agent)
		}
	}
	return out
}

// LoadAgents loads every agent definition, project shadowing personal.
//
// Same precedence as commands and settings: a project may redefine an
// agent name for its own repo without editing the user's global directory.
func LoadAgents(cwd string) []Definition {
	byName := map[string]Definition{}
	var order []string
	for _, root := range paths.ClaudeRoots(cwd) {
		scope := Project
		if root.Scope == paths.ScopeUser {
			scope = Personal
		}
		for _, agent := range loadFrom(filepath.Join(root.Dir, "agents"), scope) {
			if _, exists := byName[agent.Name]; !exists {
				order = append(order, agent.Name)
			}
			byName[agent.Name] = agent
		}
	}
	out := make([]Definition, 0, len(byName))
	for _, name := range order {
		out = append(out, byName[name])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ModelChoice identifies a provider/model pair.
type ModelChoice struct {
	ProviderID string
	ModelID    string
}

// Candidate is one model this harness could dispatch to.
type Candidate struct {
	ID       string
	Provider string
}

// ResolveAgentModel resolves an agent's `model:` field against what this
// harness is actually running on.
//
// "sonnet"/"opus"/"haiku" are Anthropic names. In a model-agnostic harness
// they cannot be requirements: on a self-hosted Ollama session there is no
// Sonnet to dispatch to, and failing the task over it would be absurd. So
// they are treated as hints:
//
//   - "provider/model" is explicit and resolved literally, if it exists.
//   - A bare alias is honored only if the parent's own provider offers a
//     matching model.
//   - "inherit", or anything unresolvable, inherits.
func ResolveAgentModel(requested string, parent ModelChoice, candidates []Candidate) ModelChoice {
	if requested == "" || requested == "inherit" {
		return parent
	}

	if slash := strings.Index(requested, "/"); slash != -1 {
		providerID := requested[:slash]
		modelID := requested[slash+1:]
		for _, c := range candidates {
			if c.Provider == providerID && c.ID == modelID {
				return ModelChoice{ProviderID: providerID, ModelID: modelID}
			}
		}
		return parent
	}

	// A bare alias is scoped to the parent's provider on purpose. Matching
	// it across every configured provider would silently move a task onto
	// a paid API because a local definition happened to say "opus".
	alias := strings.ToLower(requested)
	for _, c := range candidates {
		if c.Provider == parent.ProviderID && strings.Contains(strings.ToLower(c.ID), alias) {
			return ModelChoice{ProviderID: c.Provider, ModelID: c.ID}
		}
	}
	return parent
}
