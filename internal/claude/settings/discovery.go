package settings

// SkillDiscovery controls catalog exposure, not permission to invoke skills.
// Nil slices inherit; explicit empty arrays clear pins or exclusions.
type SkillDiscovery struct {
	IncludeAgents *bool             `json:"includeAgents,omitempty"`
	Unified       *bool             `json:"unified,omitempty"`
	Mode          string            `json:"mode,omitempty"`
	BudgetTokens  *int              `json:"budgetTokens,omitempty"`
	Pinned        []string          `json:"pinned,omitempty"`
	Excluded      []string          `json:"excluded,omitempty"`
	Sources       map[string]string `json:"-"`
}

func (d *SkillDiscovery) merge(next SkillDiscovery, source string) {
	if d.Sources == nil {
		d.Sources = map[string]string{}
	}
	if next.IncludeAgents != nil {
		v := *next.IncludeAgents
		d.IncludeAgents = &v
		d.Sources["includeAgents"] = source
	}
	if next.Unified != nil {
		v := *next.Unified
		d.Unified = &v
		d.Sources["unified"] = source
	}
	if next.Mode == "auto" || next.Mode == "search" || next.Mode == "catalog" {
		d.Mode = next.Mode
		d.Sources["mode"] = source
	}
	if next.BudgetTokens != nil && *next.BudgetTokens >= 128 && *next.BudgetTokens <= 8192 {
		n := *next.BudgetTokens
		d.BudgetTokens = &n
		d.Sources["budgetTokens"] = source
	}
	if next.Pinned != nil {
		d.Pinned = append([]string{}, next.Pinned...)
		d.Sources["pinned"] = source
	}
	if next.Excluded != nil {
		d.Excluded = append([]string{}, next.Excluded...)
		d.Sources["excluded"] = source
	}
}

func (d SkillDiscovery) Budget(tier int) int {
	n := 1024
	if d.BudgetTokens != nil {
		n = *d.BudgetTokens
	}
	if tier < n {
		return tier
	}
	return n
}
