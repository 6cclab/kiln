package money

import "testing"

// TestUSD pins the shared precision rule used by /cost's session line,
// its re-billed line, its per-model rows, and the status-line footer: two
// decimals at or above a cent, four decimals for a smaller non-zero cost
// so it is never shown as zero
// (qa/findings/20261016T122837Z-cost-session-rounds-away-subcent.json).
func TestUSD(t *testing.T) {
	cases := []struct {
		cost float64
		want string
	}{
		{0, "$0.00"},
		{0.0004, "$0.0004"},
		{0.0011, "$0.0011"},
		{0.009, "$0.0090"},
		{0.01, "$0.01"},
		{0.27, "$0.27"},
		{12.5, "$12.50"},
	}
	for _, c := range cases {
		if got := USD(c.cost); got != c.want {
			t.Errorf("USD(%v) = %q, want %q", c.cost, got, c.want)
		}
	}
}
