package permission

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
)

// Protected paths: writes that can change how code runs later (git hooks
// and config, shell startup files, package-manager and editor config) or
// how the agent itself is configured. Claude Code never auto-approves a
// write to these outside bypassPermissions
// (code.claude.com/docs/en/permission-modes, "Protected paths"): manual and
// acceptEdits ask, dontAsk refuses, auto routes them to the classifier,
// and an allow rule does not pre-approve one in any of them. kiln follows
// that in every mode (Gate.protectedWrite, CheckWithOutcome), with one list
// for all of them: Claude Code's, plus kiln's own entries marked below.
var protectedDirs = map[string]bool{
	".git": true, ".vscode": true, ".idea": true, ".husky": true, ".cargo": true,
	".devcontainer": true, ".yarn": true, ".mvn": true, ".claude": true,
	// kiln's additions: its own settings directory (.kiln, ~/.kiln, whose
	// allow rules a later session obeys), its run log and trust store
	// (~/.harness), and ssh keys and config (authorized_keys grants a
	// login, config can run a ProxyCommand).
	".kiln": true, ".harness": true, ".ssh": true,
}

// protectedPairs are directories named by their last two components
// (folded). .config/git is Claude Code's; the rest are kiln's additions
// for what starts programs at login or boot.
var protectedPairs = map[string]bool{
	".config/git":          true,
	"library/launchagents": true, "library/launchdaemons": true,
	".config/autostart": true, ".config/systemd": true,
}

var protectedFiles = map[string]bool{
	".gitconfig": true, ".gitmodules": true,
	".bashrc": true, ".bash_profile": true, ".bash_login": true, ".bash_aliases": true, ".bash_logout": true,
	".zshrc": true, ".zprofile": true, ".zshenv": true, ".zlogin": true, ".zlogout": true, ".profile": true, ".envrc": true,
	".npmrc": true, ".yarnrc": true, ".yarnrc.yml": true, ".pnp.cjs": true, ".pnp.loader.mjs": true, ".pnpmfile.cjs": true,
	"bunfig.toml": true, ".bunfig.toml": true,
	".bazelrc": true, ".bazelversion": true, ".bazeliskrc": true,
	".pre-commit-config.yaml": true, "lefthook.yml": true, "lefthook.yaml": true, ".lefthook.yml": true, ".lefthook.yaml": true,
	"gradle-wrapper.properties": true, "maven-wrapper.properties": true,
	".devcontainer.json": true, ".ripgreprc": true, "pyrightconfig.json": true,
	".mcp.json": true, ".claude.json": true,
}

// autoModeOnlyFiles are protected in auto mode only: the memory files a
// later session loads as instructions, kiln's addition to Claude Code's
// list (its .claude/rules is under .claude, protected in every mode). In
// auto mode nobody reviews the write, and the classifier itself reads
// CLAUDE.md, so a write there could steer the next review; it is
// classified. In manual and acceptEdits mode the user sees the diff, and
// keeping CLAUDE.md up to date is ordinary work Claude Code does not
// prompt for, so kiln does not either.
var autoModeOnlyFiles = map[string]bool{
	"claude.md": true, "claude.local.md": true,
}

// protection is how a write's path reaches a protected location.
type protection int

const (
	unprotected protection = iota
	// protectedAsWritten: the path as the call names it (resolved the way
	// the tool resolves it, folded as the filesystem folds names) is or
	// lies under a protected location.
	protectedAsWritten
	// protectedResolved: only what it resolves to through symlinks, or the
	// OS's own spelling of it (execenv.CanonicalPath: firmlinks, /.vol
	// paths), is protected; or it cannot be resolved at all. Auto mode asks
	// about this instead of classifying it, as Claude Code does: the path
	// the model wrote does not show the classifier what it changes.
	protectedResolved
)

// pathProtection reports how path is protected: as written, as it resolves
// through symlinks, and in the OS's own spelling of it, each compared case-
// and normalization-insensitively where the filesystem is
// (settings.CaseFoldPath, APFS folding included) and always without ASCII
// case, the same spellings deny rules are checked in. auto selects auto
// mode's list (autoModeOnlyFiles). .claude/worktrees is exempt, as in
// Claude Code.
func (g *Gate) pathProtection(path string, auto bool) protection {
	base := "."
	if len(g.roots) > 0 {
		base = g.roots[0]
	}
	full := execenv.ResolveToolPath(base, path)
	if protectedSpelling(full, auto) {
		return protectedAsWritten
	}
	real, ok := execenv.RealPath(full)
	if !ok {
		return protectedResolved // unresolvable: do not wave it through
	}
	if protectedSpelling(real, auto) || protectedSpelling(execenv.CanonicalPath(full), auto) {
		return protectedResolved
	}
	return unprotected
}

// protectedPath reports a path auto mode treats as protected, however it
// gets there.
func (g *Gate) protectedPath(path string) bool {
	return g.pathProtection(path, true) != unprotected
}

// protectedWrite returns a protected path the call writes, and how it is
// protected; unprotected when it writes none kiln can name. A file tool
// that writes (edit, write, and Claude Code's MultiEdit/NotebookEdit
// names) is judged on every path argument; a bash command line on every
// file kiln's analysis names it writing (settings.BashAutoModeWrites:
// redirections, cp/mv/tee/touch/… operands). A path protected only through
// resolution wins over one protected as written, since auto mode asks
// about the former. Commands whose writes kiln cannot name, and git
// configuration changes, are auto mode's concern only
// (bashTouchesProtected): Claude Code's other modes check the paths a
// command names, nothing more.
func (g *Gate) protectedWrite(req Request, auto bool) (string, protection) {
	var candidates []string
	switch {
	case g.mutatingFileTool(req.ToolName):
		candidates = pathArgsOf(req.Args)
	case settings.IsBashTool(req.ToolName):
		candidates, _, _ = settings.BashAutoModeWrites(req.PrimaryArg, g.cwd())
	default:
		return "", unprotected
	}
	found, how := "", unprotected
	for _, p := range candidates {
		switch g.pathProtection(p, auto) {
		case protectedResolved:
			return p, protectedResolved
		case protectedAsWritten:
			if found == "" {
				found, how = p, protectedAsWritten
			}
		}
	}
	return found, how
}

// protectedNote is why the user is asked about a protected-path write; the
// gate shows it with the prompt and gives it to the model with a refusal.
func protectedNote(path string) string {
	return fmt.Sprintf("%s is a protected path: changing it can run code or change permissions later, so it always needs the user's approval.", path)
}

// protectedRefusal is the tool result for a protected-path write nobody
// could approve (print mode, dontAsk mode): what happened and what to do
// instead, so the model does not retry the write another way.
func protectedRefusal(path, why string) string {
	return protectedNote(path) + " " + why + " It was not changed. Do not try to change it another way: tell the user the exact change it needs, so they can make it or approve it."
}

// mutatingFileTool reports a file tool that writes (edit, write, …).
func (g *Gate) mutatingFileTool(name string) bool {
	return settings.IsFileTool(name) && !settings.ReadOnly[strings.ToLower(name)]
}

// bashTouchesProtected reports a bash command line auto mode classifies
// whatever rule allows it: one that writes a protected path, changes git's
// configuration, or writes something kiln cannot name.
func (g *Gate) bashTouchesProtected(cmd string) bool {
	writes, complete, gitConfig := settings.BashAutoModeWrites(cmd, g.cwd())
	if !complete || gitConfig {
		return true
	}
	for _, w := range writes {
		if g.protectedPath(w) {
			return true
		}
	}
	return false
}

// bashProtectedOutside returns a protected path outside the workspace that
// a bash command line writes, or "": auto mode asks about that, as it does
// for a file tool writing one, instead of classifying it.
func (g *Gate) bashProtectedOutside(cmd string) string {
	writes, _, _ := settings.BashAutoModeWrites(cmd, g.cwd())
	for _, w := range writes {
		if !g.WithinRoots(w) && g.protectedPath(w) {
			return w
		}
	}
	return ""
}

// fold is a path component as protectedDirs and protectedFiles key it:
// lower-cased everywhere, and folded as the filesystem folds names where
// it does.
func fold(s string) string {
	return strings.ToLower(settings.CaseFoldPath(s))
}

func protectedSpelling(p string, auto bool) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(p)), "/")
	for i := range parts {
		parts[i] = fold(parts[i])
	}
	for i, part := range parts {
		next := ""
		if i+1 < len(parts) {
			next = parts[i+1]
		}
		if protectedDirs[part] && !(part == ".claude" && next == "worktrees") {
			return true
		}
		if protectedPairs[part+"/"+next] {
			return true
		}
	}
	last := parts[len(parts)-1]
	return protectedFiles[last] || (auto && autoModeOnlyFiles[last])
}
