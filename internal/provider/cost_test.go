package provider

import (
	"math"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func TestApplyCost(t *testing.T) {
	m := Model{Cost: ModelCost{ModelCostRates: ModelCostRates{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25}}}
	u := msg.Usage{Input: 1_000_000, Output: 100_000, CacheRead: 2_000_000, CacheWrite: 400_000, ServerToolUse: &msg.ServerToolUse{WebSearchRequests: 3}}
	ApplyCost(m, &u)
	want := 5 + 2.5 + 1 + 2.5 + 0.03
	if math.Abs(u.Cost.Total-want) > 1e-9 {
		t.Fatalf("Cost.Total = %v, want %v", u.Cost.Total, want)
	}
}
