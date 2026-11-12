package settings

// efficiencyPointers contains only comparable pointers. Successful merges
// allocate or replace them even when the winning explicit value is unchanged.
// Rejected overrides retain the preceding pointer and its source.
func efficiencyPointers(s Settings) map[string]any {
	return map[string]any{
		"learningAutomation.enabled":     s.LearningAutomation.Enabled,
		"learningAutomation.maxOutcomes": s.LearningAutomation.MaxOutcomes,
		"learningAutomation.minSamples":  s.LearningAutomation.MinSamples,
		"runtimeEfficiency.cycleLimit":   s.RuntimeEfficiency.CycleLimit,
		"taskVerificationStatus":         s.TaskVerificationStatus,
		"taskStateEnabled":               s.TaskStateEnabled, "taskStateTokens": s.TaskStateTokens,
		"learningEnabled": s.LearningEnabled, "personalMemoryEnabled": s.PersonalMemoryEnabled,
		"memoryAutoRetrieve": s.MemoryAutoRetrieve, "memoryRetrievalTokens": s.MemoryRetrievalTokens,
		"requestBudget.maxCalls": s.RequestBudget.MaxCalls, "requestBudget.maxTokens": s.RequestBudget.MaxTokens,
		"requestBudget.maxUSD":        s.RequestBudget.MaxUSD,
		"orchestration.maxConcurrent": s.Orchestration.MaxConcurrent, "orchestration.maxDispatches": s.Orchestration.MaxDispatches,
		"orchestration.maxDepth": s.Orchestration.MaxDepth, "orchestration.timeoutSeconds": s.Orchestration.TimeoutSeconds,
		"orchestration.isolatedWorkers":      s.Orchestration.IsolatedWorkers,
		"orchestration.snapshotDirty":        s.Orchestration.SnapshotDirty,
		"orchestration.autoIntegrate":        s.Orchestration.AutoIntegrate,
		"runtimeEfficiency.readCache":        s.RuntimeEfficiency.ReadCache,
		"runtimeEfficiency.readCacheEntries": s.RuntimeEfficiency.ReadCacheEntries,
		"runtimeEfficiency.readCacheBytes":   s.RuntimeEfficiency.ReadCacheBytes,
		"runtimeEfficiency.progressLimit":    s.RuntimeEfficiency.ProgressLimit,
	}
}

func (s *Settings) recordEfficiencySources(before map[string]any, raw rawSettings, source string) {
	if s.EfficiencySources == nil {
		s.EfficiencySources = map[string]string{}
	}
	for key, current := range efficiencyPointers(*s) {
		if current != before[key] {
			s.EfficiencySources[key] = source
		}
	}
	for key, source := range s.SkillDiscovery.Sources {
		s.EfficiencySources["skillDiscovery."+key] = source
	}
	if raw.RequestBudget.FreeModels != nil {
		s.EfficiencySources["requestBudget.freeModels"] = source
	}
}

// EfficiencyProvenance includes defaults for every reported efficiency field.
// It deliberately excludes environment and credential values.
func (s Settings) EfficiencyProvenance() map[string]string {
	out := map[string]string{}
	for key := range efficiencyPointers(s) {
		out[key] = "default"
	}
	for _, key := range []string{"skillDiscovery.mode", "skillDiscovery.unified", "skillDiscovery.includeAgents", "skillDiscovery.budgetTokens", "skillDiscovery.pinned", "skillDiscovery.excluded", "requestBudget.freeModels"} {
		out[key] = "default"
	}
	for key, source := range s.EfficiencySources {
		out[key] = source
	}
	return out
}
