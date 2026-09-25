// Package scenario loads eval/e2e scenario definitions (scenario.yaml plus
// whatever fixture, faux script and settings files sit alongside it) from
// disk. It is a loader only: it has no opinion about how a scenario is
// run or graded, so both test/e2e (driving the real binary) and a future
// internal/eval (an LLM-judge harness) can depend on it without pulling in
// faux, screen, or a build tag either side would rather not share.
package scenario

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// CheckTypes is the set of check "type" values Load accepts. It is a var,
// not a const, so a caller that adds its own check type (an
// internal/eval-only assertion, say) can register it before calling Load.
var CheckTypes = map[string]bool{
	"file_matches":         true,
	"file_equals":          true,
	"file_absent":          true,
	"command":              true,
	"result_ok":            true,
	"text_matches":         true,
	"tool_used":            true,
	"tool_not_used":        true,
	"tool_calls":           true,
	"no_permission_blocks": true,
	"permission_blocked":   true,
	"usage":                true,
	"compaction_occurred":  true,
	"turns":                true,
	"subagent_used":        true,
	"system_prompt_tokens": true,
}

// Caps bounds how far a scenario run is allowed to go before it is
// considered a failure regardless of what its Checks say (a runaway loop,
// an unexpectedly expensive model call).
type Caps struct {
	MaxTurns   int           `yaml:"max_turns,omitempty"`
	MaxTokens  int           `yaml:"max_tokens,omitempty"`
	MaxCostUSD float64       `yaml:"max_cost_usd,omitempty"`
	Timeout    time.Duration `yaml:"timeout,omitempty"`
	// LiveTimeout bounds a run on a real model. Timeout is sized for the
	// scripted faux server, where a turn takes milliseconds; a real model
	// needs minutes, and applying the faux cap to it kills the run mid-turn.
	LiveTimeout time.Duration `yaml:"live_timeout,omitempty"`
}

// UnmarshalYAML lets Caps decode "timeout: 120s" (a Go duration string)
// into a time.Duration, since yaml.v3 has no built-in notion of one.
func (c *Caps) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		MaxTurns    int     `yaml:"max_turns,omitempty"`
		MaxTokens   int     `yaml:"max_tokens,omitempty"`
		MaxCostUSD  float64 `yaml:"max_cost_usd,omitempty"`
		Timeout     string  `yaml:"timeout,omitempty"`
		LiveTimeout string  `yaml:"live_timeout,omitempty"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	c.MaxTurns = raw.MaxTurns
	c.MaxTokens = raw.MaxTokens
	c.MaxCostUSD = raw.MaxCostUSD
	if raw.LiveTimeout != "" {
		d, err := time.ParseDuration(raw.LiveTimeout)
		if err != nil {
			return fmt.Errorf("caps.live_timeout: %w", err)
		}
		c.LiveTimeout = d
	}
	if raw.Timeout != "" {
		d, err := time.ParseDuration(raw.Timeout)
		if err != nil {
			return fmt.Errorf("caps.timeout: %w", err)
		}
		c.Timeout = d
	}
	return nil
}

// Check is one assertion a scenario makes about a completed run. Only the
// fields relevant to Type are expected to be set; which fields those are
// is a convention between a scenario author and whatever runs Checks (this
// package does not interpret them), documented per type below:
//
//   - file_matches{Path, Regex}, file_equals{Path, Expect}, file_absent{Path}
//   - command{Run, Cwd, ExpectExit}
//   - result_ok{}, text_matches{Regex}
//   - tool_used{Name}, tool_not_used{Name}, tool_calls{Name, Min, Max}
//   - no_permission_blocks{}, permission_blocked{Name}
//   - usage{MaxInput, MaxOutput, MaxCostUSD}
//   - compaction_occurred{}, turns{Min, Max}
//   - subagent_used{Name}, system_prompt_tokens{Max}
type Check struct {
	Type string `yaml:"type"`

	Path   string `yaml:"path,omitempty"`
	Regex  string `yaml:"regex,omitempty"`
	Expect string `yaml:"expect,omitempty"`
	Run    string `yaml:"run,omitempty"`
	Cwd    string `yaml:"cwd,omitempty"`
	Name   string `yaml:"name,omitempty"`
	Role   string `yaml:"role,omitempty"`

	ExpectExit *int `yaml:"expect_exit,omitempty"`

	Min       int `yaml:"min,omitempty"`
	Max       int `yaml:"max,omitempty"`
	MaxInput  int `yaml:"max_input,omitempty"`
	MaxOutput int `yaml:"max_output,omitempty"`

	MaxCostUSD float64 `yaml:"max_cost_usd,omitempty"`
}

// Judge configures LLM-judged grading for a scenario that cannot be graded
// by mechanical Checks alone.
type Judge struct {
	// Rubric is the prompt handed to the judge model, describing what a
	// passing run looks like.
	Rubric string `yaml:"rubric,omitempty"`

	// FauxVerdict is a canned judge verdict, for scenarios run against a
	// faux judge in tests of the eval harness itself rather than a real
	// model. It accepts either a YAML string or a YAML block (mapping or
	// sequence) and stores it as JSON either way, so a caller can
	// json.Unmarshal it into whatever verdict shape it expects.
	FauxVerdict yamlAsJSON `yaml:"faux_verdict,omitempty"`
}

// yamlAsJSON decodes any YAML node (scalar, mapping, or sequence) and
// re-encodes it as JSON bytes, letting a struct field accept either a
// plain string or a structured block in its YAML source while giving Go
// code a single json.RawMessage-shaped value to work with.
type yamlAsJSON []byte

func (j *yamlAsJSON) UnmarshalYAML(value *yaml.Node) error {
	var v any
	if err := value.Decode(&v); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	*j = b
	return nil
}

// MarshalJSON lets yamlAsJSON participate in json.Marshal (e.g. if a
// Scenario is itself serialized) by emitting its bytes verbatim.
func (j yamlAsJSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// Scenario is one loaded scenario.yaml plus the directory it came from.
type Scenario struct {
	Name        string   `yaml:"name,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`

	// Fixture names a directory under a fixtures root to be copied into a
	// scratch project before the scenario runs (see CopyFixture).
	Fixture string `yaml:"fixture,omitempty"`

	Prompt         string   `yaml:"prompt"`
	PermissionMode string   `yaml:"permission_mode,omitempty"`
	AllowedTools   []string `yaml:"allowed_tools,omitempty"`

	// Settings is merged into the scratch project's .claude/settings.json
	// (permissions, modelRoles, ...) via WriteSettings.
	Settings map[string]any `yaml:"settings,omitempty"`

	Caps   Caps    `yaml:"caps,omitempty"`
	Checks []Check `yaml:"checks,omitempty"`
	Judge  *Judge  `yaml:"judge,omitempty"`

	// Faux maps an API shape (e.g. "anthropic-messages", "openai") to a
	// faux script path relative to Dir, for scenarios that script more
	// than one shape (multi-model runs, or a suite exercised against both
	// providers).
	Faux map[string]string `yaml:"faux,omitempty"`

	// Dir is the directory the scenario was loaded from, set by Load (not
	// part of the YAML).
	Dir string `yaml:"-"`
}

// Load reads dir/scenario.yaml and validates it. Name defaults to dir's
// base name when the YAML omits it. Load rejects a scenario missing
// Prompt, one using a Check.Type not in CheckTypes, or one whose Faux map
// names a script path that does not exist under dir.
func Load(dir string) (*Scenario, error) {
	path := filepath.Join(dir, "scenario.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scenario: reading %s: %w", path, err)
	}
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("scenario: parsing %s: %w", path, err)
	}
	s.Dir = dir
	if s.Name == "" {
		s.Name = filepath.Base(dir)
	}

	if s.Prompt == "" {
		return nil, fmt.Errorf("scenario %s: prompt is required", s.Name)
	}
	for _, c := range s.Checks {
		if !CheckTypes[c.Type] {
			return nil, fmt.Errorf("scenario %s: unknown check type %q", s.Name, c.Type)
		}
	}
	for api, rel := range s.Faux {
		full := filepath.Join(dir, rel)
		if _, err := os.Stat(full); err != nil {
			return nil, fmt.Errorf("scenario %s: faux[%s] = %q: %w", s.Name, api, rel, err)
		}
	}

	return &s, nil
}

// List returns every scenario found in root's immediate subdirectories
// (those containing a scenario.yaml), sorted by Scenario.Name.
func List(root string) ([]*Scenario, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("scenario: reading %s: %w", root, err)
	}
	var out []*Scenario
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "scenario.yaml")); err != nil {
			continue
		}
		s, err := Load(dir)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// FauxScript returns the contents of the faux script for the given API
// shape. When api is "" and exactly one script is registered, that script
// is returned regardless of its key.
func (s *Scenario) FauxScript(api string) (string, error) {
	if api == "" {
		switch len(s.Faux) {
		case 0:
			return "", fmt.Errorf("scenario %s: no faux script registered", s.Name)
		case 1:
			for _, rel := range s.Faux {
				return s.readFaux(rel)
			}
		default:
			return "", fmt.Errorf("scenario %s: multiple faux scripts registered, an api shape is required", s.Name)
		}
	}
	rel, ok := s.Faux[api]
	if !ok {
		return "", fmt.Errorf("scenario %s: no faux script registered for api %q", s.Name, api)
	}
	return s.readFaux(rel)
}

func (s *Scenario) readFaux(rel string) (string, error) {
	full := filepath.Join(s.Dir, rel)
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("scenario %s: reading faux script %s: %w", s.Name, full, err)
	}
	return string(data), nil
}

// CopyFixture recursively copies src into dst, preserving each file's mode
// (so an executable fixture script stays executable) and skipping any
// ".git" directory it encounters.
func CopyFixture(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		return copyFile(path, target, d)
	})
}

func copyFile(src, dst string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// WriteSettings writes projectDir/.claude/settings.json from settings,
// merging into an existing file when one is present rather than
// overwriting it: keys in settings win over keys already on disk, and
// where both sides have a map value for the same key, that one level is
// merged rather than replaced outright (so, e.g., a scenario's
// settings.permissions.allow can add to a fixture's rather than clobber
// it).
func WriteSettings(projectDir string, settings map[string]any) error {
	dir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.json")

	existing := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &existing); err != nil {
			return fmt.Errorf("scenario: parsing existing %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	merged := mergeOneLevel(existing, settings)

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// mergeOneLevel merges override into base: keys in override win, and when
// both base[k] and override[k] are maps, they are merged one level deep
// (override's keys winning); anything deeper is replaced wholesale.
func mergeOneLevel(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		if bv, ok := out[k]; ok {
			if bMap, ok := bv.(map[string]any); ok {
				if oMap, ok := v.(map[string]any); ok {
					out[k] = mergeOneLevel(bMap, oMap)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}
