package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/paths"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/router"
)

// loadModelRouter loads user-scope modelRouter/modelRoles for cwd and, when
// they resolve, registers kiln/auto (provider kiln) in reg: the one path
// `kiln`'s startup (chat.go) and the `kiln models`/`kiln providers`/
// `kiln doctor` subcommands (subcommands.go) all call, so whether kiln/auto
// is listed never drifts from whether a session would actually get it.
// sources/extraSettings mirror LoadOptions.Sources/Extra (settingsSources'
// return and --settings respectively); warn/stderr are setupModelRouter's
// own (startup warnings, and the classifier's debug line). refreshed is
// setupModelRouter's dynamic-provider dedup set (refreshRoleProvidersInto);
// pass the same map a caller's own later refreshRoleProviders call uses
// (e.g. chat.go's subagent-role refresh) so a provider both calls would
// touch refreshes only once, or nil for a one-off caller that refreshes
// nothing else.
func loadModelRouter(ctx context.Context, cwd string, reg *provider.Registry, sources []paths.Scope, extraSettings string, warn func(string), stderr io.Writer, refreshed map[string]bool) modelRouterSetup {
	routerConfig := claudesettings.LoadModelRouter(cwd, claudesettings.LoadOptions{Sources: sources, Extra: extraSettings})
	if w := claudesettings.ModelRouterIgnoredWarning(cwd, routerConfig.Ignored); w != "" {
		warn(w)
	}
	return setupModelRouter(ctx, reg, routerConfig, warn, stderr, refreshed)
}

// modelRouterSetup is kiln/auto as this run configured it.
type modelRouterSetup struct {
	// Registered reports that kiln/auto is in the registry and Routers
	// routes it. When it is not, Reason says why, for a --model or
	// resume that asks for it.
	Registered bool
	Reason     string
	Targets    router.Targets
	Routers    router.Set
	// Configured reports that the user actually set up something for the
	// router to read (a modelRouter key, or at least one modelRoles
	// entry) — doctor's gate for printing a "modelRouter" line at all,
	// the same gate the roles table already uses (len(ModelRoles) > 0),
	// so a session with neither stays quiet rather than reporting
	// "modelRouter: not available" on every run.
	Configured bool
	// classifier is the LLM classifier wired into the registered Auto
	// router, nil when the classifier is off or "fast" does not resolve.
	// Its OnUsage is set later, once the harness exists (see
	// wireClassifierUsage), as gate.SetClassifier is for auto mode's.
	classifier *router.LLMClassifier
	// CoveredRoles is every modelRoles role name (not router slot) named
	// in the combined fallback note this run printed: chat.go's own
	// ValidateRoles warnings drop a role already named here, rather than
	// print it a second time under a vaguer message.
	CoveredRoles map[string]bool
	// FallbackReasons is one phrase per distinct (provider, reason) group
	// the fallback note covered this run, in the order the note printed
	// them — "doctor"'s own "modelRouter fallback …" line and /model's
	// "· fallback: <shortest reason>" suffix are built from this rather
	// than re-parsing the note text.
	FallbackReasons []string
	// LocalCollapseNotes is the full one-model-per-local-server note
	// (collapseLocalServers), one per self-hosted server this run
	// collapsed. "doctor"'s own "modelRouter local …" line repeats them;
	// /model's describe suffix only says " · one model per local server"
	// rather than restating the whole note, which has little room for it.
	LocalCollapseNotes []string
}

// shortestFallbackReason is the shortest of FallbackReasons, "" when there
// are none: /model's describe row has little room, so it shows the most
// compact reason rather than the first or a concatenation of all of them.
func (s modelRouterSetup) shortestFallbackReason() string {
	shortest := ""
	for _, r := range s.FallbackReasons {
		if shortest == "" || len(r) < len(shortest) {
			shortest = r
		}
	}
	return shortest
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

// routerProblemKind classifies why one modelRoles value could not be used
// as a router target. Undefined and NotProviderModel are role-shape
// problems (the value itself is missing or malformed); the rest are
// operational — the role names a real provider/model, but the provider
// cannot serve it right now — and are the ones the grouped fallback note
// (warnRoleFallbacks) reports, by provider and reason, in place of a
// silent collapse.
type routerProblemKind int

const (
	problemUndefined routerProblemKind = iota
	problemNotProviderModel
	problemUnavailable
	problemUnreachable
	problemOffline
	problemNoCredentials
	problemTooSmall
)

// routerProblem is why one modelRoles value, read for role, could not be
// resolved to a usable router target.
type routerProblem struct {
	Role     string
	Provider string
	Value    string
	Kind     routerProblemKind
	Err      error
	// detail overrides reasonPhrase's default wording for this problem
	// (only Unreachable sets it, with the provider's host baked in —
	// routerTarget is the only place that can reach the provider to ask).
	detail string
	// Window is the model's context window, for TooSmall.
	Window int
	// model is the model a TooSmall problem refused as a target.
	model provider.Model
}

// String is the per-role phrase existing single-role warnings use
// ("modelRouter.roles.%s: %s.", "modelRouter.classifier: %s.", and
// kiln/auto's own "is not available" reason) — unchanged wording from
// before this type existed, for every kind but Unreachable, which is new.
func (p *routerProblem) String() string {
	switch p.Kind {
	case problemUndefined:
		return fmt.Sprintf("role %q is not in user modelRoles", p.Role)
	case problemNotProviderModel:
		return fmt.Sprintf("role %q is %q, not provider/model", p.Role, p.Value)
	case problemUnreachable:
		return fmt.Sprintf("role %q names %s, whose provider is unreachable: %v", p.Role, p.Value, p.Err)
	case problemOffline:
		return fmt.Sprintf("role %q names %s: %v", p.Role, p.Value, p.Err)
	case problemNoCredentials:
		return fmt.Sprintf("role %q names %s, whose provider has no credentials", p.Role, p.Value)
	case problemTooSmall:
		return fmt.Sprintf("role %q names %s, whose %s-token window is too small to run", p.Role, p.Value, groupThousands(p.Window))
	default: // problemUnavailable
		return fmt.Sprintf("role %q names %s, which is not an available model", p.Role, p.Value)
	}
}

// reasonPhrase is the clause the grouped fallback note uses for this
// problem's provider ("anthropic has no credentials (kiln login
// anthropic)"), independent of which role or slot hit it — every slot in
// a group shares one of these.
func (p *routerProblem) reasonPhrase() string {
	switch p.Kind {
	case problemNoCredentials:
		return fmt.Sprintf("%s has no credentials (kiln login %s)", p.Provider, p.Provider)
	case problemUnreachable:
		return p.detail
	case problemOffline:
		return fmt.Sprintf("%s is offline (HARNESS_OFFLINE)", p.Provider)
	case problemTooSmall:
		return fmt.Sprintf("%s's %s-token window is too small to run", p.Value, groupThousands(p.Window))
	case problemUnavailable:
		return fmt.Sprintf("%s has no model %s", p.Provider, p.Value)
	default:
		return fmt.Sprintf("%s is unavailable", p.Provider)
	}
}

// setupModelRouter resolves modelRouter's slots against the user's
// modelRoles and, when they resolve, registers kiln/auto (provider kiln)
// in reg with its router. A slot left at its own default role that
// modelRoles does not define at all falls back along planComplex -> plan
// -> implement and direct -> implement quietly: the user asked for
// neither a specific role there nor defined the default one, so there is
// nothing to warn about. Any other problem — a role the user named
// explicitly, or one they defined that cannot be used right now (no
// credentials, unreachable, offline, missing) — is reported in one
// combined startup note, grouped by provider and reason
// (warnRoleFallbacks), rather than silently collapsing. Without an
// implement model there is no kiln/auto at all.
//
// The roles' dynamic providers (Ollama) are refreshed first, as
// refreshRoleProviders does for subagent roles, so a role on one resolves.
func setupModelRouter(ctx context.Context, reg *provider.Registry, cfg claudesettings.ModelRouterConfig, warn func(string), stderr io.Writer, refreshed map[string]bool) modelRouterSetup {
	if refreshed == nil {
		refreshed = map[string]bool{}
	}
	for _, p := range cfg.Problems {
		warn(p)
	}
	configured := cfg.Set || len(cfg.ModelRoles) > 0
	if cfg.Disabled {
		return modelRouterSetup{Reason: `modelRouter is false in user settings`, Configured: configured}
	}
	if !cfg.Set && len(cfg.ModelRoles) == 0 {
		return modelRouterSetup{Reason: "user settings have no modelRoles for it to route to", Configured: configured}
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
	refreshErrs := refreshRoleProvidersInto(ctx, reg, values, refreshed, "", stderr)

	resolved := map[string]provider.Model{}
	problems := map[string]*routerProblem{}
	for _, slot := range claudesettings.RouterSlots {
		role := cfg.RoleFor(slot)
		if role == "" {
			continue
		}
		m, problem := routerTarget(ctx, reg, role, cfg.ModelRoles[role], refreshErrs)
		if problem != nil {
			problems[slot] = problem
			continue
		}
		resolved[slot] = m
	}

	// shouldWarn is the "did the user actually ask for this" gate: a role
	// named explicitly for the slot (modelRouter.roles.<slot>), or a
	// default role the user defined in modelRoles even though they did
	// not name it for this slot. Neither true means the slot is sitting
	// on an unmodified default whose role modelRoles never mentions at
	// all — c88cecb's quiet case. (This used to also require cfg.Set; a
	// role the user defined is worth a warning whether or not they wrote
	// a modelRouter key at all.)
	// shouldWarn's "defined" half requires a non-empty value: a role the
	// user set to "" explicitly reads the same as one modelRoles never
	// mentions at all (defaults-fail-quietly), not as "the user defined
	// it".
	shouldWarn := func(slot, role string) bool {
		_, named := cfg.Roles[slot]
		defined := cfg.ModelRoles[role] != ""
		return named || defined
	}

	implement, ok := resolved[claudesettings.RouterSlotImplement]
	if !ok {
		role := cfg.RoleFor(claudesettings.RouterSlotImplement)
		reason := fmt.Sprintf("its implement role %q names no usable model in user modelRoles", role)
		if role == "" {
			reason = "modelRouter.roles names no implement role"
		}
		if shouldWarn(claudesettings.RouterSlotImplement, role) {
			// A role-shape problem (a typo, or a value that is not
			// "provider/model") also gets its own generic line, as every
			// other slot's does; an operational one (no credentials,
			// unreachable, offline, missing) does not repeat itself —
			// there is no fallback target to describe, so the
			// "not available" reason below is the whole story.
			if p := problems[claudesettings.RouterSlotImplement]; p != nil && (p.Kind == problemUndefined || p.Kind == problemNotProviderModel) {
				warn(fmt.Sprintf("modelRouter.roles.%s: %s.", claudesettings.RouterSlotImplement, p.String()))
			}
			warn("kiln/auto is not available: " + reason + ".")
		}
		return modelRouterSetup{Reason: reason, Configured: configured}
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

	finalFor := map[string]provider.Model{
		claudesettings.RouterSlotPlan:        t.Plan,
		claudesettings.RouterSlotPlanComplex: t.PlanComplex,
		claudesettings.RouterSlotImplement:   t.Implement,
		claudesettings.RouterSlotDirect:      t.Direct,
	}
	coveredRoles, fallbackReasons := warnRoleFallbacks(problems, cfg, shouldWarn, finalFor, warn)

	// fastModel is modelRoles.fast, resolved the same way a router slot
	// is. It is not itself a router slot, but it is a collapse candidate
	// below whenever it resolves, and the model the classifier (when
	// cfg.Classifier) ends up asking -- resolved once, here, rather than
	// twice.
	fastModel, fastProblem := routerTarget(ctx, reg, "fast", cfg.ModelRoles["fast"], refreshErrs)
	// fastCollapses: fast is a collapse candidate only when it could run
	// a session itself, or a slot collapsed onto it would be a target too
	// small to run. The classifier only sends a short prompt, so a fast
	// model that is merely too small still classifies, as it did before
	// targets were size-checked.
	fastCollapses := fastProblem == nil
	if fastProblem != nil && fastProblem.Kind == problemTooSmall {
		fastModel, fastProblem = fastProblem.model, nil
	}

	t, fastModel, localNotes := collapseLocalServers(ctx, reg, t, fastModel, fastCollapses, warn)

	auto := router.AutoModel(t)
	reg.Register(router.NewVirtualProvider(auto))
	setup := modelRouterSetup{Registered: true, Targets: t, Configured: configured, CoveredRoles: coveredRoles, FallbackReasons: fallbackReasons, LocalCollapseNotes: localNotes}

	var fallback func(string) router.Complexity
	if cfg.Fallback == claudesettings.RouterFallbackHeuristic {
		fallback = router.Heuristic
	}
	autoRouter := &router.Auto{Targets: t, Fallback: fallback}
	if cfg.Classifier {
		if fastProblem == nil {
			classifier := &router.LLMClassifier{Resolve: router.ClassifierResolver(reg, fastModel)}
			autoRouter.Classifier = classifier
			setup.classifier = classifier
		} else if cfg.Set {
			warn(fmt.Sprintf("modelRouter.classifier: %s.", fastProblem))
		}
	}
	setup.Routers.Add(auto.Provider, auto.ID, autoRouter)
	return setup
}

// localRouterSlot is one router.Targets field this package's
// collapseLocalServers can read and overwrite, paired with the
// claudesettings.RouterSlot name it corresponds to for the collapse
// note.
type localRouterSlot struct {
	name string
	get  func(router.Targets) provider.Model
	set  func(*router.Targets, provider.Model)
}

var localRouterSlots = []localRouterSlot{
	{claudesettings.RouterSlotImplement,
		func(t router.Targets) provider.Model { return t.Implement },
		func(t *router.Targets, m provider.Model) { t.Implement = m }},
	{claudesettings.RouterSlotPlan,
		func(t router.Targets) provider.Model { return t.Plan },
		func(t *router.Targets, m provider.Model) { t.Plan = m }},
	{claudesettings.RouterSlotPlanComplex,
		func(t router.Targets) provider.Model { return t.PlanComplex },
		func(t *router.Targets, m provider.Model) { t.PlanComplex = m }},
	{claudesettings.RouterSlotDirect,
		func(t router.Targets) provider.Model { return t.Direct },
		func(t *router.Targets, m provider.Model) { t.Direct = m }},
}

// selfHostedHost looks up m's provider in reg and, when it implements
// provider.SelfHosted, returns it along with its Host(m) grouping key.
// ok is false for a cloud provider, or any provider without the
// capability -- this is the only place collapseLocalServers asks "is
// this local", and it asks by interface assertion, never by provider ID
// or name.
func selfHostedHost(reg *provider.Registry, m provider.Model) (provider.SelfHosted, string, bool) {
	p, ok := reg.Provider(m.Provider)
	if !ok {
		return nil, "", false
	}
	sh, ok := p.(provider.SelfHosted)
	if !ok {
		return nil, "", false
	}
	return sh, sh.Host(m), true
}

// localHostGroup is every candidate (router.LocalCandidate) this run
// found on one self-hosted server, plus the provider that server's
// Resident call goes through.
type localHostGroup struct {
	sh         provider.SelfHosted
	providerID string
	candidates []router.LocalCandidate
}

// collapseLocalServers applies Design 1, "one model per local server": a
// self-hosted server (internal/provider.SelfHosted) whose router
// targets and/or fastModel span two or more distinct models gets
// collapsed onto one of them (router.CollapseLocal), so kiln/auto never
// makes that server swap models mid-session. It queries Resident
// (/api/ps for Ollama) at most once per host, and only for a host with
// two or more candidates. t and fastModel are returned updated in
// place; a host that was not collapsed (fewer than two local candidates,
// or every one of them already resident) is returned unchanged. Every
// collapse is warned once, through warn (the same startup-note path
// warnRoleFallbacks uses), and also returned as notes for doctor.
func collapseLocalServers(ctx context.Context, reg *provider.Registry, t router.Targets, fastModel provider.Model, fastResolves bool, warn func(string)) (router.Targets, provider.Model, []string) {
	groups := map[string]*localHostGroup{}
	var order []string
	add := func(slot string, m provider.Model) {
		sh, host, ok := selfHostedHost(reg, m)
		if !ok {
			return
		}
		g, exists := groups[host]
		if !exists {
			g = &localHostGroup{sh: sh, providerID: m.Provider}
			groups[host] = g
			order = append(order, host)
		}
		g.candidates = append(g.candidates, router.LocalCandidate{Slot: slot, Model: m})
	}
	for _, s := range localRouterSlots {
		add(s.name, s.get(t))
	}
	if fastResolves {
		add("fast", fastModel)
	}

	var notes []string
	for _, host := range order {
		g := groups[host]
		if distinctLocalModels(g.candidates) < 2 {
			continue
		}
		var resident []string
		if ids, err := g.sh.Resident(ctx); err == nil {
			resident = ids
		}
		collapse, changed := router.CollapseLocal(g.candidates, resident)
		if !changed {
			continue
		}
		for _, c := range g.candidates {
			if c.Slot == "fast" {
				fastModel = collapse.Chosen
				continue
			}
			for _, s := range localRouterSlots {
				if s.name == c.Slot {
					s.set(&t, collapse.Chosen)
				}
			}
		}
		note := localCollapseNote(reg, g.providerID, host, g.candidates, collapse)
		warn(note)
		notes = append(notes, note)
	}
	return t, fastModel, notes
}

// distinctLocalModels counts the distinct provider/id models among
// candidates, so collapseLocalServers only calls Resident for a host
// with two or more of them -- a host where every slot (and fast)
// already names the same model has nothing to collapse, and asking
// /api/ps there would be a wasted round trip.
func distinctLocalModels(candidates []router.LocalCandidate) int {
	seen := map[string]bool{}
	for _, c := range candidates {
		seen[c.Model.Provider+"/"+c.Model.ID] = true
	}
	return len(seen)
}

// localCollapseNote is the startup note for one collapsed host, e.g.
// "kiln/auto: the Ollama server at 192.168.3.168:11434 keeps one model
// loaded, so plan and planComplex use qwen3.8 (already loaded) instead
// of qwen3:30b-a3b and gpt-oss:20b. Keep all of them loaded to route
// between them." Only candidates whose model actually changed are named
// ("the classifier" for fast, since it is not itself a modelRouter slot).
func localCollapseNote(reg *provider.Registry, providerID, host string, candidates []router.LocalCandidate, collapse router.LocalCollapse) string {
	providerName := providerID
	if p, ok := reg.Provider(providerID); ok {
		providerName = p.Name()
	}
	addr := strings.TrimPrefix(host, providerID+" ")
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")

	var changed []string
	for _, c := range candidates {
		if c.Model.Provider == collapse.Chosen.Provider && c.Model.ID == collapse.Chosen.ID {
			continue
		}
		if c.Slot == "fast" {
			changed = append(changed, "the classifier")
			continue
		}
		changed = append(changed, c.Slot)
	}

	chosenLabel := collapse.Chosen.ID
	if collapse.AlreadyLoaded {
		chosenLabel += " (already loaded)"
	}
	var otherIDs []string
	for _, m := range collapse.Others {
		otherIDs = append(otherIDs, m.ID)
	}

	return fmt.Sprintf("kiln/auto: the %s server at %s keeps one model loaded, so %s use %s instead of %s. Keep all of them loaded to route between them.",
		providerName, addr, joinAnd(changed), chosenLabel, joinAnd(otherIDs))
}

// fallbackGroup is every slot whose target collapsed to the same
// provider for the same reason — "anthropic has no credentials" covering
// both plan and planComplex, say — reported as one sentence instead of
// one warning per slot.
type fallbackGroup struct {
	reason string
	slots  []string
	roles  map[string]bool
	// origValues and finals keep slot order, so "plan and planComplex use
	// … instead of X and Y" pairs X with plan and Y with planComplex.
	origValues []string
	finals     []string
}

func (g *fallbackGroup) sentence() string {
	slots := joinAnd(g.slots)
	finals := joinAnd(g.finals)
	origs := joinAnd(g.origValues)
	return fmt.Sprintf("%s, so %s use %s instead of %s", g.reason, slots, finals, origs)
}

// warnRoleFallbacks turns every slot-level problem the gate approves into
// warnings: a role-shape problem (undefined or not-provider-model) keeps
// its own line, exactly as before this phase; every operational problem
// (no credentials, unreachable, offline, missing) is grouped by provider
// and reason and folded into one combined "kiln/auto: …" note — Design
// 3's "exactly one note per run". It returns the modelRoles role names
// the note named (for chat.go's ValidateRoles dedupe) and one reason
// phrase per group (for doctor and /model's describe suffix).
func warnRoleFallbacks(problems map[string]*routerProblem, cfg claudesettings.ModelRouterConfig, shouldWarn func(slot, role string) bool, finalFor map[string]provider.Model, warn func(string)) (map[string]bool, []string) {
	groups := map[string]*fallbackGroup{}
	var order []string
	coveredRoles := map[string]bool{}
	for _, slot := range claudesettings.RouterSlots {
		p, ok := problems[slot]
		if !ok || slot == claudesettings.RouterSlotImplement {
			continue
		}
		role := cfg.RoleFor(slot)
		if !shouldWarn(slot, role) {
			continue
		}
		if p.Kind == problemUndefined || p.Kind == problemNotProviderModel {
			warn(fmt.Sprintf("modelRouter.roles.%s: %s.", slot, p.String()))
			continue
		}
		key := fmt.Sprintf("%d|%s|%v", p.Kind, p.Provider, p.Err)
		if p.Kind == problemTooSmall {
			// Each too-small model is its own reason: its phrase names it.
			key += "|" + p.Value
		}
		g, exists := groups[key]
		if !exists {
			g = &fallbackGroup{reason: p.reasonPhrase(), roles: map[string]bool{}}
			groups[key] = g
			order = append(order, key)
		}
		g.slots = append(g.slots, slot)
		g.roles[p.Role] = true
		coveredRoles[p.Role] = true
		if p.Value != "" {
			g.origValues = appendUnique(g.origValues, p.Value)
		}
		if m, ok := finalFor[slot]; ok {
			g.finals = appendUnique(g.finals, m.Provider+"/"+m.ID)
		}
	}
	if len(order) == 0 {
		return coveredRoles, nil
	}
	var sentences []string
	var reasons []string
	allRoles := map[string]bool{}
	for _, key := range order {
		g := groups[key]
		sentences = append(sentences, g.sentence())
		reasons = append(reasons, g.reason)
		for r := range g.roles {
			allRoles[r] = true
		}
	}
	note := "kiln/auto: " + strings.Join(sentences, "; ") + "."
	if len(allRoles) > 0 {
		note += " Subagents asking for " + joinAnd(sortedKeys(allRoles)) + " run on the current model."
	}
	warn(note)
	return coveredRoles, reasons
}

// joinAnd renders a list as English prose: "a", "a and b", "a, b and c".
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}

// appendUnique appends v unless list already holds it.
func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// sortedKeys returns a set's members sorted, for deterministic prose.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// routerTarget resolves one modelRoles value for the router: a physical
// model in the registry whose provider has credentials and, under
// HARNESS_OFFLINE=1, is local. refreshErrs is setupModelRouter's
// per-provider refresh outcome (refreshRoleProvidersInto): when value's
// provider failed to refresh and so has no models to find value in, the
// problem is reported as unreachable (with the provider's host, when it
// exposes one) rather than the more generic "not an available model".
func routerTarget(ctx context.Context, reg *provider.Registry, role, value string, refreshErrs map[string]error) (provider.Model, *routerProblem) {
	if value == "" {
		return provider.Model{}, &routerProblem{Role: role, Kind: problemUndefined}
	}
	providerID, modelID, ok := splitProviderModel(value)
	if !ok {
		return provider.Model{}, &routerProblem{Role: role, Value: value, Kind: problemNotProviderModel}
	}
	m, ok := reg.GetModel(providerID, modelID)
	if !ok || provider.IsVirtual(m) {
		if err, bad := refreshErrs[providerID]; bad {
			detail := fmt.Sprintf("%s unreachable: %v", providerID, err)
			if p, ok := reg.Provider(providerID); ok {
				if u, ok := p.(interface{ BaseURL() string }); ok {
					detail = fmt.Sprintf("%s at %s unreachable: %v", providerID, u.BaseURL(), err)
				}
			}
			return provider.Model{}, &routerProblem{Role: role, Provider: providerID, Value: value, Kind: problemUnreachable, Err: err, detail: detail}
		}
		return provider.Model{}, &routerProblem{Role: role, Provider: providerID, Value: value, Kind: problemUnavailable}
	}
	if err := offlineGuard(providerID); err != nil {
		return provider.Model{}, &routerProblem{Role: role, Provider: providerID, Value: value, Kind: problemOffline, Err: err}
	}
	if hasAuth, err := reg.CheckAuth(ctx, providerID); err != nil || !hasAuth {
		return provider.Model{}, &routerProblem{Role: role, Provider: providerID, Value: value, Kind: problemNoCredentials}
	}
	// A request routed to m is sized for m's own window (agent.Retier), so
	// a window no session could run in (Ollama's 8,192 fallback, say) makes
	// m unusable as a target: the slot falls back like any other, rather
	// than refusing kiln/auto as a whole.
	if _, err := budget.RequireTierForWindow(m.ContextWindow); err != nil {
		return provider.Model{}, &routerProblem{Role: role, Provider: providerID, Value: value, Kind: problemTooSmall, Err: err, Window: m.ContextWindow, model: m}
	}
	return m, nil
}

// groupThousands renders n with comma thousands separators ("8,192").
func groupThousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
