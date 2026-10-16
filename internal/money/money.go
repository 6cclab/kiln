// Package money renders USD costs with one precision rule shared by every
// place kiln shows a dollar amount: the /cost command (session line,
// re-billed line, per-model rows) and the status-line footer.
//
// Two decimals ($0.27) read fine once a cost clears a cent, but a
// non-zero cost under a cent rounds to "$0.00" at that precision — the
// session total next to a $0.0011 per-model row ("$0.00" above "$0.0011")
// read as if nothing had been spent
// (qa/findings/20261016T122837Z-cost-session-rounds-away-subcent.json).
// USD keeps the per-model row's existing four-decimal precision for that
// case instead of inventing a new one.
package money

import "fmt"

// USD formats a cost in US dollars: "$0.00" at exactly zero, "$0.27" at
// two decimals once the magnitude is at least one cent, and four decimals
// ("$0.0011") for a smaller non-zero amount, so it is never shown as zero.
func USD(cost float64) string {
	if cost == 0 {
		return "$0.00"
	}
	abs := cost
	if abs < 0 {
		abs = -abs
	}
	if abs < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}
