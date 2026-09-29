package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

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

// Error reads as a sentence a person can act on: the status in words and
// digits, the provider's own message when the body is the JSON error
// envelope every supported API returns, and the request id to quote to
// support. A body that is not that envelope is kept, whitespace-collapsed
// and capped, so nothing the provider said is lost.
func (e *StatusError) Error() string {
	head := statusText(e.Status) + " (" + strconv.Itoa(e.Status) + ")"
	message, requestID := parseErrorBody(e.Body)
	if message == "" {
		message = strings.Join(strings.Fields(e.Body), " ")
		if r := []rune(message); len(r) > maxRawErrorBody {
			message = string(r[:maxRawErrorBody]) + "…"
		}
	}
	out := head
	// "Overloaded (529): Overloaded" says nothing twice.
	if message != "" && !strings.EqualFold(strings.TrimRight(message, "."), statusText(e.Status)) {
		out += ": " + message
	}
	if requestID != "" {
		out += " · request " + requestID
	}
	return out
}

// maxRawErrorBody caps a non-JSON error body (an HTML error page from a
// proxy, say) in the message.
const maxRawErrorBody = 300

func statusText(code int) string {
	switch code {
	case 529:
		return "Overloaded"
	case 0:
		return "Request failed"
	}
	if t := http.StatusText(code); t != "" {
		return t
	}
	return "HTTP error"
}

// parseErrorBody reads the error envelope: Anthropic's
// {"type":"error","error":{"type","message"},"request_id"}, OpenAI's and
// Mistral's {"error":{"message","type","code"}}, Google's
// {"error":{"code","message","status"}}, and a bare {"message"} or
// {"detail"}.
func parseErrorBody(body string) (message, requestID string) {
	var env struct {
		Error     json.RawMessage `json:"error"`
		Message   string          `json:"message"`
		Detail    string          `json:"detail"`
		RequestID string          `json:"request_id"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &env) != nil {
		return "", ""
	}
	requestID = env.RequestID
	var inner struct {
		Message string `json:"message"`
	}
	var str string
	switch {
	case json.Unmarshal(env.Error, &inner) == nil && inner.Message != "":
		message = inner.Message
	case json.Unmarshal(env.Error, &str) == nil && str != "":
		message = str
	case env.Message != "":
		message = env.Message
	default:
		message = env.Detail
	}
	return strings.Join(strings.Fields(message), " "), requestID
}

func isRetriableStatus(status int) bool {
	return status == 429 || status == 529 || status >= 500
}
