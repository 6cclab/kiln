package tools

import (
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tool"
)

// Builtins returns the four built-in tools every session gets for free,
// in pi's order (bash, read, edit, write — see tools/index.js), all
// operating against env.
func Builtins(env *execenv.Env) *tool.Set {
	return tool.NewSet(
		BashTool(env),
		ReadTool(env),
		EditTool(env),
		WriteTool(env),
	)
}
