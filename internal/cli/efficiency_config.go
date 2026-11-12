package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"

	settings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/learning"
)

// EfficiencyConfig prints only non-secret efficiency settings; it must never
// marshal Settings wholesale because Settings.Env may contain credentials.
func EfficiencyConfig(args Args, out, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		writeDiagnostic(stderr, err)
		return 1
	}
	opts := settings.LoadOptions{Sources: settingsSources(args.SettingSources), Extra: args.Settings, Trusted: folderTrusted(cwd), Headless: true}
	s := settings.LoadSettings(cwd, opts)
	requestedModel, _ := requestedModelAndSource(cwd, args, s)
	runtimeSettings := effectiveContextSettings(cwd, args, s, modelAcceptsEffort(requestedModel))
	var learned *learning.Active
	if s.LearningEnabled == nil || *s.LearningEnabled {
		active, err := compatiblePolicy(context.Background(), cwd, s.ModelRoles)
		if err != nil {
			writeDiagnostic(stderr, "Learned policy unavailable:", err)
		} else if active != nil {
			learned = active
			selected := args.Model
			if selected == "" {
				selected = s.Model
			}
			if selected == "" {
				selected = os.Getenv("HARNESS_MODEL")
			}
			if selected == "" {
				selected = defaultModel
			}
			if s.SkillDiscovery.BudgetTokens == nil && policyCoversModel(active, selected) {
				n := active.Policy.SkillBudgetTokens
				s.SkillDiscovery.BudgetTokens = &n
				if s.SkillDiscovery.Sources == nil {
					s.SkillDiscovery.Sources = map[string]string{}
				}
				s.SkillDiscovery.Sources["budgetTokens"] = "evaluated personal policy"
			}
		}
	}
	mode := s.SkillDiscovery.Mode
	if mode == "" {
		mode = "auto"
	}
	budget := 1024
	if s.SkillDiscovery.BudgetTokens != nil {
		budget = *s.SkillDiscovery.BudgetTokens
	}
	router := settings.LoadModelRouter(cwd, opts)
	applyLearnedRouting(&router, learned)
	sources := runtimeSettings.Sources
	for key, source := range s.SkillDiscovery.Sources {
		sources["skillDiscovery."+key] = source
	}
	sources["modelRouter.conservative"] = "default"
	if router.ConservativeSource != "" {
		sources["modelRouter.conservative"] = router.ConservativeSource
	}
	report := map[string]any{
		"contextSettings": map[string]any{
			"model": runtimeSettings.Model, "effortLevel": runtimeSettings.Effort,
			"permissionMode":      runtimeSettings.Permission,
			"autoMemoryDirectory": runtimeSettings.AutoMemoryDirectory,
		},
		"configuredSettings": map[string]any{
			"modelRoles":              s.ModelRoles,
			"autoMemoryEnabled":       s.AutoMemoryEnabled == nil || *s.AutoMemoryEnabled,
			"promptSuggestionEnabled": s.PromptSuggestionEnabled != nil && *s.PromptSuggestionEnabled,
		},
		"taskVerificationStatus":  s.TaskVerificationStatus == nil || *s.TaskVerificationStatus,
		"taskStateEnabled":        s.TaskStateEnabled == nil || *s.TaskStateEnabled,
		"taskStateTokens":         settings.PositiveOr(s.TaskStateTokens, 512),
		"skillDiscovery":          map[string]any{"includeAgents": s.SkillDiscovery.IncludeAgents == nil || *s.SkillDiscovery.IncludeAgents, "mode": mode, "unified": s.SkillDiscovery.Unified == nil || *s.SkillDiscovery.Unified, "budgetTokens": budget, "pinned": s.SkillDiscovery.Pinned, "excluded": s.SkillDiscovery.Excluded, "sources": s.SkillDiscovery.Sources},
		"personalMemoryEnabled":   s.PersonalMemoryEnabled == nil || *s.PersonalMemoryEnabled,
		"memoryRetrievalTokens":   settings.PositiveOr(s.MemoryRetrievalTokens, 512),
		"memoryAutoRetrieve":      s.MemoryAutoRetrieve == nil || *s.MemoryAutoRetrieve,
		"requestBudget":           s.RequestBudget,
		"orchestration":           map[string]any{"isolatedWorkers": s.Orchestration.IsolatedWorkers != nil && *s.Orchestration.IsolatedWorkers, "isolatedShell": s.Orchestration.IsolatedShell != nil && *s.Orchestration.IsolatedShell, "snapshotDirty": s.Orchestration.SnapshotDirty != nil && *s.Orchestration.SnapshotDirty, "autoIntegrate": s.Orchestration.AutoIntegrate != nil && *s.Orchestration.AutoIntegrate, "maxConcurrent": settings.PositiveOr(s.Orchestration.MaxConcurrent, 4), "maxDispatches": settings.PositiveOr(s.Orchestration.MaxDispatches, 64), "maxDepth": settings.PositiveOr(s.Orchestration.MaxDepth, 2), "timeoutSeconds": settings.PositiveOr(s.Orchestration.TimeoutSeconds, 1800)},
		"conservativeRouting":     router.Conservative,
		"learningAutomation":      map[string]any{"enabled": s.LearningAutomation.Enabled != nil && *s.LearningAutomation.Enabled, "maxOutcomes": settings.PositiveOr(s.LearningAutomation.MaxOutcomes, 100), "minSamples": settings.PositiveOr(s.LearningAutomation.MinSamples, 5)},
		"learningEnabled":         s.LearningEnabled == nil || *s.LearningEnabled,
		"compatibleLearnedPolicy": learned,
		"loadedFrom":              s.LoadedFrom,
		"sources":                 sources,
		"runtimeEfficiency":       map[string]any{"readCache": s.RuntimeEfficiency.ReadCache != nil && *s.RuntimeEfficiency.ReadCache, "readCacheEntries": settings.PositiveOr(s.RuntimeEfficiency.ReadCacheEntries, 32), "readCacheBytes": settings.PositiveOr(s.RuntimeEfficiency.ReadCacheBytes, 4<<20), "progressLimit": settings.PositiveOr(s.RuntimeEfficiency.ProgressLimit, 0), "progressLoopLimit": settings.PositiveOr(s.RuntimeEfficiency.ProgressLoopLimit, 0), "cycleLimit": settings.PositiveOr(s.RuntimeEfficiency.CycleLimit, 0)},
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		writeDiagnostic(stderr, err)
		return 1
	}
	return 0
}
