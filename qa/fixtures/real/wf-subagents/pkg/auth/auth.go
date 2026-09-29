// Package auth checks API tokens against a fixed allowlist.
//
// Deliberate error-handling gap (for the QA audit prompt): Check swallows
// the lookup error entirely instead of returning it, so a broken store looks
// identical to "token not found".
package auth

import "errors"

var ErrInvalidToken = errors.New("auth: invalid token")

type Store interface {
	Lookup(token string) (bool, error)
}

func Check(s Store, token string) bool {
	ok, err := s.Lookup(token)
	if err != nil {
		return false // bug: the caller can never tell "denied" from "store broke"
	}
	return ok
}
