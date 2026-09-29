// Package payments charges a fixed-price order against a gateway.
//
// Deliberate error-handling gap (for the QA audit prompt): Charge retries on
// any error, including ones a gateway returns for a declined card, which are
// not transient and should not be retried.
package payments

import "time"

type Gateway interface {
	Charge(cents int64) error
}

func Charge(g Gateway, cents int64) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = g.Charge(cents)
		if err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}
