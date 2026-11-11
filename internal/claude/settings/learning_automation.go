package settings

// LearningAutomation controls local, aggregate run observations and reviewable
// suggestions. Explicit enablement is required; it never activates policies.
type LearningAutomation struct {
	Enabled     *bool `json:"enabled,omitempty"`
	MaxOutcomes *int  `json:"maxOutcomes,omitempty"`
	MinSamples  *int  `json:"minSamples,omitempty"`
}

func (a *LearningAutomation) merge(next LearningAutomation) {
	if next.Enabled != nil {
		v := *next.Enabled
		a.Enabled = &v
	}
	candidate := *a
	if n := next.MaxOutcomes; n != nil && *n >= 1 && *n <= 1000 {
		v := *n
		candidate.MaxOutcomes = &v
	}
	if n := next.MinSamples; n != nil && *n >= 2 && *n <= 100 {
		v := *n
		candidate.MinSamples = &v
	}
	// Reject inconsistent numeric overrides together, preserving prior values
	// and provenance instead of silently changing either requested setting.
	if PositiveOr(candidate.MinSamples, 5) <= PositiveOr(candidate.MaxOutcomes, 100) {
		a.MaxOutcomes, a.MinSamples = candidate.MaxOutcomes, candidate.MinSamples
	}
}
