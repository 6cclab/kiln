package settings

// recordSource stores the last accepted explicit source for a setting.
func (s *Settings) recordSource(key, source string) {
	if s.Sources == nil {
		s.Sources = make(map[string]string)
	}
	s.Sources[key] = source
}

// Provenance reports defaults and effective explicit sources for settings
// surfaced alongside /context. Environment values are deliberately omitted.
func (s Settings) Provenance() map[string]string {
	out := s.EfficiencyProvenance()
	for _, key := range []string{
		"model", "effortLevel", "permissions.defaultMode", "statusLine",
		"autoMemoryEnabled", "promptSuggestionEnabled", "autoMemoryDirectory",
	} {
		out[key] = "default"
	}
	for key := range s.ModelRoles {
		out["modelRoles."+key] = "default"
	}
	for key, source := range s.Sources {
		out[key] = source
	}
	return out
}
