package mcp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"testing"
)

func TestDescribeConnectError(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
		err  error
		want string
	}{
		{"not found on PATH", ServerConfig{Command: "tsx"}, fmt.Errorf("start: %w", exec.ErrNotFound), "command not found: tsx"},
		{"missing path", ServerConfig{Command: "/x/tsx"}, &fs.PathError{Op: "fork/exec", Path: "/x/tsx", Err: fs.ErrNotExist}, "command not found: /x/tsx"},
		{"timeout", ServerConfig{}, context.DeadlineExceeded, "no response within " + connectTimeout().String()},
		// No headers configured at all: nothing to "check", so this reads
		// as OAuth-only and says so instead of claiming a token exists
		// (qa/findings/20261004T203042Z-mcp-oauth-server-says-check-
		// token.json: Sentry, an OAuth-only server with no token
		// configured, used to get "unauthorized (check its token)").
		{"unauthorized http, no token configured", ServerConfig{URL: "https://mcp.sentry.dev/mcp"}, errors.New(`calling "initialize": sending "initialize": Unauthorized`), "needs authentication (OAuth sign-in isn't supported yet; sign in once with Claude Code, or add a token under this server's \"headers\") at https://mcp.sentry.dev/mcp"},
		// A header that looks like a credential is configured: a 401 here
		// really is "check its token" (it's probably wrong or expired).
		{"unauthorized http, token configured", ServerConfig{URL: "https://mcp.example/sse", Headers: map[string]string{"Authorization": "Bearer abc"}}, errors.New(`calling "initialize": sending "initialize": Unauthorized`), "unauthorized (check its token) at https://mcp.example/sse"},
		{"refused", ServerConfig{URL: "http://localhost:1/mcp"}, errors.New("dial tcp: connection refused"), "connection refused at http://localhost:1/mcp"},
	}
	for _, c := range cases {
		if got := describeConnectError(c.cfg, c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
