package cli

import (
	"context"
	"fmt"
	"io"

	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/router"
)

// modelRouterSetup is kiln/auto as this run configured it.
type modelRouterSetup struct {
	// Registered reports that kiln/auto is in the registry and Routers
	// routes it. When it is not, Reason says why, for a --model or
	// resume that asks for it.
	Registered bool
	Reason     string
	Targets    router.Targets
	Routers    router.Set
	// classifier is the LLM classifier wired into the registered Auto
	// router, nil when the classifier is off or "fast" does not resolve.
	// Its OnUsage is set later, once the harness exists (see
	// wireClassifierUsage), as gate.SetClassifier is for auto mode's.
	classifier *router.LLMClassifier
}

// approved reports whether providerID/modelID is one of kiln/auto's
// targets: the user chose them in user settings, so a subagent sent to
// one needs no second approval (agent.Dispatcher.Approved).
func (s modelRouterSetup) approved(providerID, modelID string) bool {
	if !s.Registered {
		return false
	}
	for _, m := range s.Targets.All() {
		if m.Provider == providerID && m.ID == modelID {
			return true
		}
	}
	return false
}

// direct is kiln/auto's direct target, for auto mode's classifier when
// modelRoles.fast is unset; ok is false when kiln/auto is not registered.
func (s modelRouterSetup) direct() (provider.Model, bool) {
	return s.Targets.Direct, s.Registered
}

// wireClassifierUsage binds the model router classifier's usage into the
// session's cost, once the harness that can record it exists. It mirrors
// wireAutoMode's late binding of the gate's classifier: setupModelRouter
// runs before the harness is built, so this is called afterward with
// Harness.RecordSideUsage.
func (s modelRouterSetup) wireClassifierUsage(record func(source string, u msg.Usage) error) {
	if s.classifier == nil {
		return
	}
	s.classifier.OnUsage = func(model provider.Model, u msg.Usage) {
		if err := record(ModelRouterClassifierUsageSource(model), u); err != nil {
			diag.L().Warn("model router classifier usage", "err", err)
		}
	}
}

// ModelRouterClassifierUsageSource is how /cost labels the model router
// classifier's spend on model.
func ModelRouterClassifierUsageSource(model provider.Model) string {
	return "model router classifier · " + model.Provider + "/" + model.ID
}

// setupModelRouter resolves modelRouter's slots against the user's
// modelRoles and, when they resolve, registers kiln/auto (provider kiln)
// in reg with its router. A slot without a model falls back along
// planComplex -> plan -> implement and direct -> implement; without an
// implement model there is no kiln/auto. Problems with a modelRouter the
// user wrote are passed to warn; the defaults (no modelRouter key) fail
// quietly, since modelRoles alone does not ask for a router.
//
// The roles' dynamic providers (Ollama) are refreshed first, as
// refreshRoleProviders does for subagent roles, so a role on one resolves.
func setupModelRouter(ctx context.Context, reg *provider.Registry, cfg claudesettings.ModelRouterConfig, warn func(string), stderr io.Writer) modelRouterSetup {
	for _, p := range cfg.Problems {
		warn(p)
	}
	if cfg.Disabled {
		return modelRouterSetup{Reason: `modelRouter is false in user settings`}
	}
	if !cfg.Set && len(cfg.ModelRoles) == 0 {
		return modelRouterSetup{Reason: "user settings have no modelRoles for it to route to"}
	}
	values := map[string]string{}
	for _, slot := range claudesettings.RouterSlots {
		if role := cfg.RoleFor(slot); role != "" && cfg.ModelRoles[role] != "" {
			values[slot] = cfg.ModelRoles[role]
		}
	}
	// "fast" is the classifier's own role, read directly (it is not a
	// router slot), and is refreshed here too so it resolves even when no
	// slot happens to use it.
	if cfg.ModelRoles["fast"] != "" {
		values["modelRouterFast"] = cfg.ModelRoles["fast"]
	}
	refreshRoleProviders(ctx, reg, values, "", stderr)

	resolved := map[string]provider.Model{}
	for _, slot := range claudesettings.RouterSlots {
		role := cfg.RoleFor(slot)
		if role == "" {
			continue
		}
		m, problem := routerTarget(ctx, reg, role, cfg.ModelRoles[role])
		if problem != "" {
			if cfg.Set {
				warn(fmt.Sprintf("modelRouter.roles.%s: %s.", slot, problem))
			}
			continue
		}
		resolved[slot] = m
	}
	implement, ok := resolved[claudesettings.RouterSlotImplement]
	if !ok {
		reason := fmt.Sprintf("its implement role %q names no usable model in user modelRoles", cfg.RoleFor(claudesettings.RouterSlotImplement))
		if cfg.RoleFor(claudesettings.RouterSlotImplement) == "" {
			reason = "modelRouter.roles names no implement role"
		}
		if cfg.Set {
			warn("kiln/auto is not available: " + reason + ".")
		}
		return modelRouterSetup{Reason: reason}
	}
	pick := func(slot string, fallback provider.Model) provider.Model {
		if m, ok := resolved[slot]; ok {
			return m
		}
		return fallback
	}
	var t router.Targets
	t.Implement = implement
	t.Plan = pick(claudesettings.RouterSlotPlan, t.Implement)
	t.PlanComplex = pick(claudesettings.RouterSlotPlanComplex, t.Plan)
	t.Direct = pick(claudesettings.RouterSlotDirect, t.Implement)

	auto := router.AutoModel(t)
	reg.Register(router.NewVirtualProvider(auto))
	setup := modelRouterSetup{Registered: true, Targets: t}

	var fallback func(string) router.Complexity
	if cfg.Fallback == claudesettings.RouterFallbackHeuristic {
		fallback = router.Heuristic
	}
	autoRouter := &router.Auto{Targets: t, Fallback: fallback}
	if cfg.Classifier {
		fastModel, problem := routerTarget(ctx, reg, "fast", cfg.ModelRoles["fast"])
		if problem == "" {
			classifier := &router.LLMClassifier{Resolve: router.ClassifierResolver(reg, fastModel)}
			autoRouter.Classifier = classifier
			setup.classifier = classifier
		} else if cfg.Set {
			warn(fmt.Sprintf("modelRouter.classifier: %s.", problem))
		}
	}
	setup.Routers.Add(auto.Provider, auto.ID, autoRouter)
	return setup
}

// routerTarget resolves one modelRoles value for the router: a physical
// model in the registry whose provider has credentials and, under
// HARNESS_OFFLINE=1, is local. problem says why not, as a phrase.
func routerTarget(ctx context.Context, reg *provider.Registry, role, value string) (provider.Model, string) {
	if value == "" {
		return provider.Model{}, fmt.Sprintf("role %q is not in user modelRoles", role)
	}
	providerID, modelID, ok := splitProviderModel(value)
	if !ok {
		return provider.Model{}, fmt.Sprintf("role %q is %q, not provider/model", role, value)
	}
	m, ok := reg.GetModel(providerID, modelID)
	if !ok || provider.IsVirtual(m) {
		return provider.Model{}, fmt.Sprintf("role %q names %s, which is not an available model", role, value)
	}
	if err := offlineGuard(providerID); err != nil {
		return provider.Model{}, fmt.Sprintf("role %q names %s: %v", role, value, err)
	}
	if hasAuth, err := reg.CheckAuth(ctx, providerID); err != nil || !hasAuth {
		return provider.Model{}, fmt.Sprintf("role %q names %s, whose provider has no credentials", role, value)
	}
	return m, ""
}
