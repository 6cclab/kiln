package api

import "strconv"

// Auth is the resolved credential used for one request. Exactly one of
// APIKey or OAuthAccessToken is meaningful, selected by IsOAuth.
type Auth struct {
	// APIKey is sent as-is (Anthropic: x-api-key header; OpenAI-completions:
	// Authorization: Bearer).
	APIKey string
	// IsOAuth marks APIKey as an Anthropic OAuth access token rather than a
	// plain API key, switching to Bearer auth plus the Claude Code identity
	// / beta headers pi sends for subscription auth.
	IsOAuth bool
	// Headers are extra headers merged over the client defaults (caller
	// values win), matching pi's `options.headers` / `model.headers`.
	Headers map[string]string
}

// StatusError is an HTTP-level failure from the provider, carrying whether a
// retry is worth attempting (429 and 5xx, matching pi's retry policy; 529 is
// Anthropic's "overloaded").
type StatusError struct {
	Status    int
	Body      string
	Retriable bool
}

func (e *StatusError) Error() string {
	return "provider request failed: status=" + strconv.Itoa(e.Status) + " body=" + e.Body
}

func isRetriableStatus(status int) bool {
	return status == 429 || status == 529 || status >= 500
}
