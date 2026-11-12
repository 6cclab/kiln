package settings

// RequestBudget controls all model calls in a running session, including helpers.
// Zero disables a ceiling; nil inherits it from the preceding settings scope.
type RequestBudget struct {
	MaxCalls   *int     `json:"maxCalls,omitempty"`
	MaxTokens  *int64   `json:"maxTokens,omitempty"`
	MaxUSD     *float64 `json:"maxUSD,omitempty"`
	FreeModels []string `json:"freeModels,omitempty"`
}

func (b *RequestBudget) merge(next RequestBudget) {
	if next.MaxCalls != nil && *next.MaxCalls >= 0 {
		v := *next.MaxCalls
		b.MaxCalls = &v
	}
	if next.MaxTokens != nil && *next.MaxTokens >= 0 {
		v := *next.MaxTokens
		b.MaxTokens = &v
	}
	if next.MaxUSD != nil && *next.MaxUSD >= 0 {
		v := *next.MaxUSD
		b.MaxUSD = &v
	}
	if next.FreeModels != nil {
		b.FreeModels = append([]string{}, next.FreeModels...)
	}
}

// mergeRestrictive applies repository-scope settings without allowing them to
// loosen a user budget. Zero means no ceiling, so it cannot replace a
// positive inherited value. A repository may remove free-model exemptions,
// but cannot add an exemption the user did not grant.
// It reports whether an explicit freeModels list was accepted as an override.
func (b *RequestBudget) mergeRestrictive(next RequestBudget) bool {
	if next.MaxCalls != nil && *next.MaxCalls >= 0 && (b.MaxCalls == nil || *b.MaxCalls == 0 || (*next.MaxCalls > 0 && *next.MaxCalls <= *b.MaxCalls)) {
		v := *next.MaxCalls
		b.MaxCalls = &v
	}
	if next.MaxTokens != nil && *next.MaxTokens >= 0 && (b.MaxTokens == nil || *b.MaxTokens == 0 || (*next.MaxTokens > 0 && *next.MaxTokens <= *b.MaxTokens)) {
		v := *next.MaxTokens
		b.MaxTokens = &v
	}
	if next.MaxUSD != nil && *next.MaxUSD >= 0 && (b.MaxUSD == nil || *b.MaxUSD == 0 || (*next.MaxUSD > 0 && *next.MaxUSD <= *b.MaxUSD)) {
		v := *next.MaxUSD
		b.MaxUSD = &v
	}
	if next.FreeModels == nil {
		return false
	}
	allowed := make(map[string]bool, len(next.FreeModels))
	for _, model := range next.FreeModels {
		allowed[model] = true
	}
	kept := make([]string, 0, len(b.FreeModels))
	for _, model := range b.FreeModels {
		if allowed[model] {
			kept = append(kept, model)
		}
	}
	if len(kept) == len(b.FreeModels) && len(next.FreeModels) > len(kept) {
		return false // Only additions were requested; preserve prior provenance.
	}
	b.FreeModels = kept
	return true
}
