package api

import "testing"

func TestStatusErrorReadsAsSentence(t *testing.T) {
	cases := []struct {
		name string
		err  StatusError
		want string
	}{
		{"anthropic envelope", StatusError{Status: 400, Body: `{"type":"error","error":{"type":"invalid_request_error","message":"\"thinking.type.disabled\" is not supported for this model."},"request_id":"req_011abc"}`},
			`Bad Request (400): "thinking.type.disabled" is not supported for this model. · request req_011abc`},
		{"openai envelope", StatusError{Status: 401, Body: `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`},
			"Unauthorized (401): Incorrect API key provided"},
		{"overloaded", StatusError{Status: 529, Body: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
			"Overloaded (529)"},
		{"html page", StatusError{Status: 502, Body: "<html>\n  <body>Bad gateway</body>\n</html>"},
			"Bad Gateway (502): <html> <body>Bad gateway</body> </html>"},
		{"empty body", StatusError{Status: 503}, "Service Unavailable (503)"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
