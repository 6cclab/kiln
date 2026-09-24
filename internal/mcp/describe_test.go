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
		{"unauthorized http", ServerConfig{URL: "https://mcp.example/sse"}, errors.New(`calling "initialize": sending "initialize": Unauthorized`), "unauthorized (check its token) at https://mcp.example/sse"},
		{"refused", ServerConfig{URL: "http://localhost:1/mcp"}, errors.New("dial tcp: connection refused"), "connection refused at http://localhost:1/mcp"},
	}
	for _, c := range cases {
		if got := describeConnectError(c.cfg, c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
