package provider

import "github.com/andrepato/harness/internal/msg"

// ApplyCost fills u.Cost from model's per-million-token rates, matching
// pi's calculateCost for the (non-tiered) common case, plus Anthropic's
// $10 per 1000 web searches.
func ApplyCost(model Model, u *msg.Usage) {
	rates := model.Cost.ModelCostRates
	u.Cost = msg.Cost{
		Input:      float64(u.Input) / 1_000_000 * rates.Input,
		Output:     float64(u.Output) / 1_000_000 * rates.Output,
		CacheRead:  float64(u.CacheRead) / 1_000_000 * rates.CacheRead,
		CacheWrite: float64(u.CacheWrite) / 1_000_000 * rates.CacheWrite,
	}
	if u.ServerToolUse != nil {
		u.Cost.Search = float64(u.ServerToolUse.WebSearchRequests) / 1000 * 10
	}
	u.Cost.Total = u.Cost.Input + u.Cost.Output + u.Cost.CacheRead + u.Cost.CacheWrite + u.Cost.Search
}
