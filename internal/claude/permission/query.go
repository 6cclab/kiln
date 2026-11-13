package permission

import (
	"context"
	"errors"
)

// A query asks the gate what it would decide without asking anybody: no
// prompt is shown and no classifier model is called (kiln's mod
// $.tool.check). Where the gate would put the call to a person or to the
// classifier, the query answers "ask".

type queryKey struct{}

// ErrWouldAsk is what a query's check returns where the gate would prompt.
var ErrWouldAsk = errors.New("permission: the call would be put to the prompt")

// WithQuery marks ctx as a query.
func WithQuery(ctx context.Context) context.Context {
	return context.WithValue(ctx, queryKey{}, true)
}

func isQuery(ctx context.Context) bool {
	q, _ := ctx.Value(queryKey{}).(bool)
	return q
}

// QueryVerdict is a query's answer: Decision is allow, ask or deny, with the
// gate's reason for a deny.
type QueryVerdict struct {
	Decision string
	Reason   string
}

// Query is what the gate would do with req, asking nobody. A deny is also
// recorded in Blocked, as any refusal is.
func (g *Gate) Query(ctx context.Context, req Request) (QueryVerdict, error) {
	blocked, _, err := g.checkWithOutcome(WithQuery(ctx), req)
	switch {
	case errors.Is(err, ErrWouldAsk):
		return QueryVerdict{Decision: "ask"}, nil
	case err != nil:
		return QueryVerdict{}, err
	case blocked != nil:
		return QueryVerdict{Decision: "deny", Reason: blocked.Reason}, nil
	}
	return QueryVerdict{Decision: "allow"}, nil
}
