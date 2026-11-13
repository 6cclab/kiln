package cli

import (
	"context"
	"strings"
	"time"

	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/harness"
)

// describeTools raises tool.describe for each of the main conversation's
// tools and swaps in the description a hook rewrote. A subagent's own tools
// are built elsewhere and keep theirs.
func (r *modsRuntime) describeTools(ctx context.Context, h *harness.Harness) {
	e := r.get()
	if e == nil || !e.Has("tool.describe") {
		return
	}
	set := h.Tools()
	for _, n := range set.Names() {
		t, ok := set.Get(n)
		if !ok || r.isModTool(n) {
			continue
		}
		if d := e.ToolDescribe(ctx, claudeToolName(n), t.Description, strings.HasPrefix(n, "mcp__")); d != "" {
			cp := *t
			cp.Description = d
			h.AddTools(&cp) // a copy: requests read the shared tool concurrently
		}
	}
}

// describeCommand raises command.describe for one command; the registry
// calls it each time its list is rebuilt. A slow hook costs at most a
// moment of the typeahead's first draw.
func (r *modsRuntime) describeCommand(c slashcommands.Command) slashcommands.Command {
	e := r.get()
	if e == nil || !e.Has("command.describe") {
		return c
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	c.Description, c.ArgumentHint = e.CommandDescribe(ctx, slashcommands.QualifiedName(c), c.Description, c.ArgumentHint)
	return c
}
