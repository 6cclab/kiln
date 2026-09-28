package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/plural"
)

// Posture is one MCP search posture /posture can switch to (coding, ops,
// all, ...). The catalog and tool-counting logic live in internal/cli,
// which is this package's only caller for InlineCommands; Posture is kept
// minimal on purpose so this package does not need to know what a posture
// actually gates.
type Posture struct {
	Name        string
	Description string
}

// InlineDeps is everything InlineCommands binds against. These three
// commands (posture, bashes, todos) register inline in cli.ts rather than
// living in their own commands/*.ts file; InlineCommands is this port's
// equivalent registration point.
type InlineDeps struct {
	Postures      []Posture
	ActivePosture func() string
	// PostureToolCount reports how many tools a posture makes searchable,
	// for the listing.
	PostureToolCount func(name string) int
	// SwitchPosture applies a posture switch: the integrator clears the
	// admitted-tools set and re-runs session.lane.SetActiveTools under the
	// new posture, mirroring cli.ts's inline /posture handler.
	SwitchPosture func(ctx context.Context, name string) error

	// RenderShellList renders the background-shell list for /bashes. Nil
	// until phase 6 (internal/agent/background-shell equivalent) supplies
	// it; /bashes then reports there are none rather than failing.
	RenderShellList func() []string

	Todos *agent.TodoStore
}

// InlineCommands returns the source for /posture, /bashes and /todos.
//
// This /todos registers after AccountCommands' in cli.ts's order, so by
// Registry.List's later-wins rule it shadows that one; both render
// identically today, but this is the copy the integrator should point the
// TodoStore at going forward.
func InlineCommands(deps InlineDeps) Source {
	cmds := []Command{
		{
			Name:         "posture",
			Description:  "Show or change which MCP servers are searchable",
			ArgumentHint: "[coding|ops|all]",
			ArgumentCompletions: func(prefix string) []Completion {
				trimmed := strings.TrimSpace(prefix)
				var out []Completion
				for _, p := range deps.Postures {
					if !strings.HasPrefix(p.Name, trimmed) {
						continue
					}
					count := 0
					if deps.PostureToolCount != nil {
						count = deps.PostureToolCount(p.Name)
					}
					out = append(out, Completion{Value: p.Name, Label: p.Name, Description: plural.Count(count, "tool")})
				}
				return out
			},
			Run: func(ctx context.Context, args string) (Result, error) {
				trimmed := strings.TrimSpace(args)
				active := ""
				if deps.ActivePosture != nil {
					active = deps.ActivePosture()
				}
				if trimmed == "" {
					// The current posture is marked the way /model marks the
					// current model: a check after its name.
					lines := []string{"Postures decide which MCP servers tool search reaches.", ""}
					for _, p := range deps.Postures {
						name := p.Name
						if p.Name == active {
							name += " ✓"
						}
						count := 0
						if deps.PostureToolCount != nil {
							count = deps.PostureToolCount(p.Name)
						}
						lines = append(lines, fmt.Sprintf("  %-10s %9s  %s", name, plural.Count(count, "tool"), p.Description))
					}
					lines = append(lines, "", "Switch with /posture <name>.")
					return Result{Output: lines}, nil
				}

				var found *Posture
				for i := range deps.Postures {
					if deps.Postures[i].Name == trimmed {
						found = &deps.Postures[i]
						break
					}
				}
				if found == nil {
					names := make([]string, len(deps.Postures))
					for i, p := range deps.Postures {
						names[i] = p.Name
					}
					return Result{Output: []string{fmt.Sprintf(`Unknown posture "%s". Options: %s`, args, strings.Join(names, ", "))}}, nil
				}
				if deps.SwitchPosture != nil {
					if err := deps.SwitchPosture(ctx, found.Name); err != nil {
						return Result{}, err
					}
				}
				count := 0
				if deps.PostureToolCount != nil {
					count = deps.PostureToolCount(found.Name)
				}
				return Result{Output: []string{fmt.Sprintf("Posture: %s (%d tools searchable). Admitted tools cleared.", found.Name, count)}}, nil
			},
		},
		{
			Name:        "bashes",
			Description: "Show background shells and their status",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.RenderShellList == nil {
					return Result{Output: []string{"no background shells"}}, nil
				}
				return Result{Output: deps.RenderShellList()}, nil
			},
		},
		{
			Name:        "todos",
			Description: "Show the current todo list",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.Todos == nil {
					return Result{Output: []string{"No todos."}}, nil
				}
				items := deps.Todos.Get()
				if len(items) == 0 {
					return Result{Output: []string{"No todos."}}, nil
				}
				return Result{Output: renderTodos(items)}, nil
			},
		},
	}

	return StaticSource(OriginBuiltin, cmds)
}
