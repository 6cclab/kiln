package settings

// LearningReview controls kiln's automatic learning review
// (internal/review): after EveryPrompts prompts or EveryToolCalls tool
// calls, a background request proposes skill and memory changes that wait
// for approval in /pending. Off unless the user's own settings enable it.
type LearningReview struct {
	Enabled        *bool `json:"enabled,omitempty"`
	EveryPrompts   *int  `json:"everyPrompts,omitempty"`
	EveryToolCalls *int  `json:"everyToolCalls,omitempty"`
}

// Review cadence defaults, hermes-agent's memory and skill nudge intervals.
const (
	DefaultReviewEveryPrompts   = 10
	DefaultReviewEveryToolCalls = 15
)

func (r *LearningReview) merge(next LearningReview) {
	if next.Enabled != nil {
		v := *next.Enabled
		r.Enabled = &v
	}
	if n := next.EveryPrompts; n != nil && *n >= 1 && *n <= 1000 {
		v := *n
		r.EveryPrompts = &v
	}
	if n := next.EveryToolCalls; n != nil && *n >= 1 && *n <= 10000 {
		v := *n
		r.EveryToolCalls = &v
	}
}
