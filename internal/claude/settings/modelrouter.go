package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/andrepato/harness/internal/claude/paths"
)

// The model router's slots (kiln's built-in kiln/auto, internal/router):
// the model that plans a complex request, the one that plans any other,
// the one that implements once an edit has landed, and the one for
// requests outside the turn loop (compaction summaries). pi's jev router
// calls them Sol, Terra, Luna and Luna.
const (
	RouterSlotPlan        = "plan"
	RouterSlotPlanComplex = "planComplex"
	RouterSlotImplement   = "implement"
	RouterSlotDirect      = "direct"
)

// RouterSlots lists every slot, in the order the docs and warnings use.
var RouterSlots = []string{RouterSlotPlan, RouterSlotPlanComplex, RouterSlotImplement, RouterSlotDirect}

// DefaultRouterRoles is the slot -> modelRoles name map used when
// modelRouter names no roles of its own.
var DefaultRouterRoles = map[string]string{
	RouterSlotPlan:        "structured",
	RouterSlotPlanComplex: "heavy",
	RouterSlotImplement:   "fast",
	RouterSlotDirect:      "fast",
}

// Router fallbacks: what picks the planning model when the classifier is
// off, unavailable or gives no answer. "plan" is pi's (jev-router.ts's
// Terra); "heuristic" is kiln's own.
const (
	RouterFallbackPlan      = "plan"
	RouterFallbackHeuristic = "heuristic"
)

// ModelRouterConfig is settings.json's "modelRouter" key, read only from
// the files LoadAutoMode trusts (user settings and --settings), together
// with modelRoles from those same files, which its role names resolve
// against.
type ModelRouterConfig struct {
	// Set reports that a trusted file has a modelRouter key. When it is
	// not set the router still runs on DefaultRouterRoles if ModelRoles
	// resolves them.
	Set bool
	// Disabled is "modelRouter": false.
	Disabled bool
	// Roles maps a slot (RouterSlots) to a modelRoles name. Nil when the
	// block names no roles, which means DefaultRouterRoles. A slot left
	// out falls back along planComplex -> plan -> implement and direct ->
	// implement.
	Roles map[string]string
	// Classifier is whether the planning model is picked by a classifier
	// call on the fast role (true unless the block says false).
	Classifier bool
	// Fallback is RouterFallbackPlan or RouterFallbackHeuristic.
	Fallback string
	// ModelRoles is modelRoles merged from the trusted files only: a
	// repository must not choose the model every request goes to.
	ModelRoles map[string]string
	// Ignored lists project and local settings files with a modelRouter
	// key, for a startup warning.
	Ignored []string
	// Problems are values kiln could not use, one sentence each, for
	// startup warnings.
	Problems []string
}

// RoleFor is the modelRoles name configured for slot, "" when none is.
func (c ModelRouterConfig) RoleFor(slot string) string {
	if c.Roles == nil {
		return DefaultRouterRoles[slot]
	}
	return c.Roles[slot]
}

type rawModelRouter struct {
	Roles      map[string]string `json:"roles"`
	Classifier *bool             `json:"classifier"`
	Fallback   *string           `json:"fallback"`
}

// LoadModelRouter reads "modelRouter" and "modelRoles" from user settings
// (~/.claude/settings.json, then ~/.kiln/settings.json) and the --settings
// file, the same trusted sources as LoadAutoMode and for the same reason:
// the router decides which, possibly paid, model every request goes to,
// and a cloned repository must not decide that. A modelRouter key in
// project or local settings is reported in Ignored. A later file's
// modelRouter replaces an earlier one's; modelRoles merges key by key, as
// LoadSettings merges it.
func LoadModelRouter(cwd string, opts LoadOptions) ModelRouterConfig {
	cfg := ModelRouterConfig{Classifier: true, Fallback: RouterFallbackPlan}
	type fileValues struct {
		router json.RawMessage
		roles  map[string]string
	}
	read := func(path string) (fileValues, bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			return fileValues{}, false
		}
		var raw struct {
			ModelRouter json.RawMessage   `json:"modelRouter"`
			ModelRoles  map[string]string `json:"modelRoles"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return fileValues{}, false
		}
		return fileValues{router: raw.ModelRouter, roles: raw.ModelRoles}, true
	}
	add := func(path string, v fileValues) {
		for k, val := range v.roles {
			if cfg.ModelRoles == nil {
				cfg.ModelRoles = map[string]string{}
			}
			cfg.ModelRoles[k] = val
		}
		if len(v.router) == 0 {
			return
		}
		cfg.applyRouter(path, v.router)
	}
	for _, f := range paths.AllSettingsFiles(cwd) {
		v, ok := read(f.Path)
		if !ok {
			continue
		}
		if f.Scope != paths.ScopeUser {
			if len(v.router) > 0 {
				cfg.Ignored = append(cfg.Ignored, f.Path)
			}
			continue
		}
		if wants(opts.Sources, f.Scope) {
			add(f.Path, v)
		}
	}
	if opts.Extra != "" {
		if v, ok := read(opts.Extra); ok {
			add(opts.Extra, v)
		}
	}
	return cfg
}

// applyRouter replaces cfg's router values with one file's modelRouter.
func (cfg *ModelRouterConfig) applyRouter(path string, raw json.RawMessage) {
	trimmed := bytes.TrimSpace(raw)
	switch string(trimmed) {
	case "null":
		return
	case "false":
		cfg.Set, cfg.Disabled = true, true
		cfg.Roles, cfg.Classifier, cfg.Fallback = nil, true, RouterFallbackPlan
		return
	case "true":
		cfg.Set, cfg.Disabled = true, false
		cfg.Roles, cfg.Classifier, cfg.Fallback = nil, true, RouterFallbackPlan
		return
	}
	var r rawModelRouter
	if err := json.Unmarshal(trimmed, &r); err != nil {
		cfg.Problems = append(cfg.Problems, fmt.Sprintf("modelRouter in %s is not false or an object; ignored", path))
		return
	}
	cfg.Set, cfg.Disabled = true, false
	cfg.Roles, cfg.Classifier, cfg.Fallback = nil, true, RouterFallbackPlan
	if r.Roles != nil {
		cfg.Roles = map[string]string{}
		var unknown []string
		for slot, role := range r.Roles {
			if _, ok := DefaultRouterRoles[slot]; !ok {
				unknown = append(unknown, slot)
				continue
			}
			cfg.Roles[slot] = role
		}
		sort.Strings(unknown)
		for _, slot := range unknown {
			cfg.Problems = append(cfg.Problems, fmt.Sprintf("modelRouter.roles.%s in %s is not a router slot (plan, planComplex, implement, direct); ignored", slot, path))
		}
	}
	if r.Classifier != nil {
		cfg.Classifier = *r.Classifier
	}
	if r.Fallback != nil {
		switch *r.Fallback {
		case RouterFallbackPlan, RouterFallbackHeuristic:
			cfg.Fallback = *r.Fallback
		default:
			cfg.Problems = append(cfg.Problems, fmt.Sprintf("modelRouter.fallback %q in %s is not \"plan\" or \"heuristic\"; using \"plan\"", *r.Fallback, path))
		}
	}
}

// ModelRouterIgnoredWarning is the startup warning for modelRouter keys
// kiln did not read, "" when there are none.
func ModelRouterIgnoredWarning(cwd string, ignored []string) string {
	if len(ignored) == 0 {
		return ""
	}
	name := autoModeIgnoredName(cwd, ignored[0])
	if len(ignored) > 1 {
		name += " and other project settings"
	}
	return "modelRouter in " + name + " is ignored: kiln reads it only from ~/.claude/settings.json, ~/.kiln/settings.json or --settings, so a repository cannot choose the models every request goes to."
}
