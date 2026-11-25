package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestMCPReconnect: /mcp reconnect <server|all> reconnects in the
// background and answers in Claude Code's words; anything else is refused
// without touching a server.
func TestMCPReconnect(t *testing.T) {
	var asked []string
	deps := ManageDeps{
		MCPServerNames: func() []string { return []string{"web", "db"} },
		MCPReconnect: func(_ context.Context, name string) (ServerStatus, error) {
			asked = append(asked, name)
			switch name {
			case "db":
				return ServerStatus{Name: name, OK: true, ToolCount: 3}, nil
			case "web":
				return ServerStatus{Name: name, Error: "connection refused"}, nil
			}
			return ServerStatus{}, errors.New(`there's no MCP server named "` + name + `"`)
		},
	}
	run := func(args string) []string {
		res := mcpAction(deps, args)
		if res.Background == nil {
			return res.Output
		}
		out, err := res.Background(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return out.Output
	}

	if got := run("reconnect all"); !reflect.DeepEqual(got, []string{`Reconnected "db" (3 tools).`, `Couldn't reconnect "web": connection refused`}) {
		t.Errorf("reconnect all = %q", got)
	}
	if got := run("reconnect nope"); len(got) != 1 || got[0] != `there's no MCP server named "nope". Run /mcp to see the configured servers.` {
		t.Errorf("reconnect nope = %q", got)
	}
	asked = nil
	if got := run("restart db"); len(got) != 1 || got[0] != `"restart" isn't a recognized /mcp action. Try reconnect.` || asked != nil {
		t.Errorf("restart db = %q (asked %v)", got, asked)
	}
	if got := run("reconnect"); got[0] != "Usage: /mcp reconnect <server|all>" {
		t.Errorf("reconnect with no server = %q", got)
	}
	if res := mcpAction(deps, "reconnect db"); res.BusyLabel != "Reconnecting db" {
		t.Errorf("busy label = %q", res.BusyLabel)
	}
}
