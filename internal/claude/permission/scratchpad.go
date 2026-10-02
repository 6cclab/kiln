package permission

import (
	"strings"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
)

// The session scratchpad (execenv.EnsureScratchpad): a directory the model
// may read and write without a prompt in every mode, as Claude Code allows
// its session scratchpad in every mode. Deny and ask rules still apply
// first, and a path is inside only when it is inside both as written and
// as it resolves through symlinks (Gate.within), so a symlink planted in
// the scratchpad that points elsewhere is gated like the place it points
// to.

// SetScratchpad makes dir the session scratchpad; other spellings of the
// same directory (unresolved, /tmp for /private/tmp) may follow, so a path
// written either way is inside as written. No arguments clears it.
func (g *Gate) SetScratchpad(dir string, spellings ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.scratchpad = nil
	if dir == "" {
		return
	}
	g.scratchpad = append([]string{dir}, spellings...)
}

// Scratchpad is the session scratchpad, "" when there is none.
func (g *Gate) Scratchpad() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.scratchpad) == 0 {
		return ""
	}
	return g.scratchpad[0]
}

// InScratchpad reports a path inside the session scratchpad, as written
// and through symlinks. A file there with another hard link is not inside:
// the same file has a name elsewhere (the workspace, a dotfile), and no
// path check can say where. Deny rules and the workspace check are
// path-based and do not look at link counts; the scratchpad does because
// it is the one place a call skips every prompt.
func (g *Gate) InScratchpad(path string) bool {
	g.mu.Lock()
	roots := g.scratchpad
	g.mu.Unlock()
	if !g.within(path, roots) {
		return false
	}
	base := "."
	if len(g.roots) > 0 {
		base = g.roots[0]
	}
	return !execenv.SharedFile(execenv.ResolveToolPath(base, path))
}

// scratchpadCall reports a call that only touches the scratchpad: a file
// tool whose one path argument is inside it, or a bash command that writes
// only there and reads only there or in the workspace. Never past a deny
// or ask rule, nor a command kiln cannot fully name.
func (g *Gate) scratchpadCall(req Request, hits settings.Hits) bool {
	if hits.Deny || hits.Ask || hits.Unsure || g.Scratchpad() == "" {
		return false
	}
	name := strings.ToLower(req.ToolName)
	switch {
	case settings.IsFileTool(name):
		path, ok := PathArgOf(req.Args)
		return ok && g.InScratchpad(path)
	case settings.IsBashTool(name):
		return settings.BashWritesOnlyInside(req.PrimaryArg, g.cwd(), g.InScratchpad, g.WithinRoots)
	}
	return false
}
