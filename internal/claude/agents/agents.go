package agents

import (
	"context"
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
	// Plugin marks a Definition built from a plugin's agents/*.md by
	// internal/claude/plugins, rather than loaded here by LoadAgents.
	Plugin Source = "plugin"
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

// ResolveKind labels why ResolveModel landed on the ModelChoice it
// returned. Diagnostics (the subagent start event, `/model roles`,
// `doctor`) use it to say more than just "here's the model" - e.g. that a
// request fell back to the parent rather than actually resolving.
type ResolveKind string

const (
	// ResolveInherited means requested was empty or "inherit".
	ResolveInherited ResolveKind = "inherited"
	// ResolveRole means requested named a key of the roles map and that
	// role's value resolved against candidates.
	ResolveRole ResolveKind = "role"
	// ResolveExplicit means requested was a literal "provider/model" that
	// resolved against candidates.
	ResolveExplicit ResolveKind = "explicit"
	// ResolveAlias means requested was a bare alias (built-in or
	// substring-matched against the parent's provider) that resolved.
	ResolveAlias ResolveKind = "alias"
	// ResolveFallback means requested named something that could not be
	// resolved, so the parent's model was kept.
	ResolveFallback ResolveKind = "fallback"
)

// Chooser is the seam for a future classifier that picks a model role
// (e.g. "fast", "heavy") from a subagent's description and prompt, rather
// than requiring every agent definition to spell out its own `model:`.
// ResolveModel does not call this in this phase - it is defined so that
// seam exists on the API, and ships unused; a nil Chooser is the correct
// value everywhere today, and callers should treat it as "inherit".
type Chooser interface {
	// Choose returns a role name (a key ResolveModel would look up in its
	// roles map) for the given subagent description/prompt, or ok=false to
	// mean "no opinion, inherit".
	Choose(ctx context.Context, description, prompt string) (role string, ok bool)
}

// builtinAliasRoles maps Claude Code's Anthropic-shaped bare aliases onto
// this harness's own role names, so a definition that says "model: sonnet"
// can be redirected by modelRoles.structured without every .md file in
// existence being rewritten.
var builtinAliasRoles = map[string]string{
	"haiku":  "fast",
	"sonnet": "structured",
	"opus":   "heavy",
}

// splitProviderModelValue splits a "provider/model" role value into its two
// halves. Deliberately local to this package rather than shared with
// internal/cli's splitProviderModel: the two are allowed to diverge (this
// one, for instance, need not reject a model id that itself contains "/").
func splitProviderModelValue(v string) (providerID, modelID string, ok bool) {
	idx := strings.IndexByte(v, '/')
	if idx <= 0 || idx == len(v)-1 {
		return "", "", false
	}
	return v[:idx], v[idx+1:], true
}

// resolveRoleValue resolves one modelRoles value ("provider/model")
// against candidates, reporting whether it named something real.
func resolveRoleValue(value string, candidates []Candidate) (ModelChoice, bool) {
	providerID, modelID, ok := splitProviderModelValue(value)
	if !ok {
		return ModelChoice{}, false
	}
	for _, c := range candidates {
		if c.Provider == providerID && c.ID == modelID {
			return ModelChoice{ProviderID: providerID, ModelID: modelID}, true
		}
	}
	return ModelChoice{}, false
}

// ResolveModel resolves an agent's (or /model's) requested model string
// against what this harness is actually running on, honoring per-role
// overrides from settings.json's modelRoles ahead of the built-in alias
// behavior.
//
// Precedence:
//
//  1. "" or "inherit" -> the parent's model.
//  2. requested is a key of roles -> that role's "provider/model" value,
//     if it resolves against candidates; else the parent (fallback).
//  3. requested contains "/" -> resolved literally against candidates as
//     an explicit provider/model; else the parent (fallback).
//  4. A bare alias (e.g. "sonnet"): if its built-in role
//     (haiku->fast, sonnet->structured, opus->heavy) is configured in
//     roles, resolved as in (2); otherwise the same-provider substring
//     match this harness has always done, or the parent if nothing
//     matches.
//
// "sonnet"/"opus"/"haiku" are Anthropic names. In a model-agnostic harness
// they cannot be requirements: on a self-hosted Ollama session there is no
// Sonnet to dispatch to, and failing the task over it would be absurd. So
// they, and roles, are always treated as hints that fall back to the
// parent rather than erroring.
func ResolveModel(requested string, roles map[string]string, parent ModelChoice, candidates []Candidate) (ModelChoice, ResolveKind) {
	if requested == "" || requested == "inherit" {
		return parent, ResolveInherited
	}

	if roleValue, ok := roles[requested]; ok {
		if choice, found := resolveRoleValue(roleValue, candidates); found {
			return choice, ResolveRole
		}
		return parent, ResolveFallback
	}

	if slash := strings.Index(requested, "/"); slash != -1 {
		providerID := requested[:slash]
		modelID := requested[slash+1:]
		for _, c := range candidates {
			if c.Provider == providerID && c.ID == modelID {
				return ModelChoice{ProviderID: providerID, ModelID: modelID}, ResolveExplicit
			}
		}
		return parent, ResolveFallback
	}

	alias := strings.ToLower(requested)
	if role, ok := builtinAliasRoles[alias]; ok {
		if roleValue, ok := roles[role]; ok {
			if choice, found := resolveRoleValue(roleValue, candidates); found {
				return choice, ResolveAlias
			}
			return parent, ResolveFallback
		}
	}

	// A bare alias with no matching role is scoped to the parent's
	// provider on purpose. Matching it across every configured provider
	// would silently move a task onto a paid API because a local
	// definition happened to say "opus".
	for _, c := range candidates {
		if c.Provider == parent.ProviderID && strings.Contains(strings.ToLower(c.ID), alias) {
			return ModelChoice{ProviderID: c.Provider, ModelID: c.ID}, ResolveAlias
		}
	}
	return parent, ResolveFallback
}

// ResolveAgentModel is ResolveModel with no roles configured, kept so
// existing callers (and their tests) that have no modelRoles to pass
// still compile and behave exactly as before.
func ResolveAgentModel(requested string, parent ModelChoice, candidates []Candidate) ModelChoice {
	choice, _ := ResolveModel(requested, nil, parent, candidates)
	return choice
}

// ValidateRoles checks every configured role's value against candidates,
// returning one human-readable problem per role that fails - either
// because the value is not "provider/model" shaped, or because it names no
// available model - sorted by role name. `doctor` and startup warnings use
// this; ResolveModel itself never reports failures, it only falls back.
//
// Each returned string is "<role>: <detail>", so a caller building
// "kiln: role <name>: <problem>" needs only prepend "kiln: role ".
func ValidateRoles(roles map[string]string, candidates []Candidate) []string {
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)

	var problems []string
	for _, name := range names {
		value := roles[name]
		providerID, modelID, ok := splitProviderModelValue(value)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: %q is not provider/model", name, value))
			continue
		}
		found := false
		for _, c := range candidates {
			if c.Provider == providerID && c.ID == modelID {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("%s: %s is not among the available models", name, value))
		}
	}
	return problems
}
