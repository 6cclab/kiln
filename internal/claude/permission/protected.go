package permission

import (
	"path/filepath"
	"strings"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
)

// Protected paths: writes that can change how code runs later (git hooks
// and config, shell startup files, package-manager and editor config) or
// how the agent itself is configured. Claude Code never auto-approves a
// write to these; in auto mode it routes them to the classifier, even past
// an allow rule (code.claude.com/docs/en/permission-modes, "Protected
// paths"). kiln follows that in auto mode, and adds its own .kiln.
var protectedDirs = map[string]bool{
	".git": true, ".vscode": true, ".idea": true, ".husky": true, ".cargo": true,
	".devcontainer": true, ".yarn": true, ".mvn": true, ".claude": true, ".kiln": true,
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
	// Memory files a later session loads as instructions: kiln's addition
	// to Claude Code's list (its own .claude/rules is under .claude).
	"claude.md": true, "claude.local.md": true,
}

// protectedPath reports a path that is or lies under a protected directory,
// or names a protected file: as written, as it resolves through symlinks,
// and in the OS's own spelling of it (execenv.CanonicalPath: firmlinks,
// /.vol paths), each compared case- and normalization-insensitively where
// the filesystem is (settings.CaseFoldPath, APFS folding included) — the
// same spellings deny rules are checked in. .claude/worktrees is exempt,
// as in Claude Code.
func (g *Gate) protectedPath(path string) bool {
	base := "."
	if len(g.roots) > 0 {
		base = g.roots[0]
	}
	full := execenv.ResolveToolPath(base, path)
	real, ok := execenv.RealPath(full)
	if !ok {
		return true // unresolvable: do not wave it through
	}
	for _, p := range []string{full, real, execenv.CanonicalPath(full)} {
		if protectedSpelling(p) {
			return true
		}
	}
	return false
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

// fold is a path component as protectedDirs and protectedFiles key it:
// lower-cased everywhere, and folded as the filesystem folds names where
// it does.
func fold(s string) string {
	return strings.ToLower(settings.CaseFoldPath(s))
}

func protectedSpelling(p string) bool {
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
		if part == ".config" && next == "git" {
			return true
		}
	}
	return protectedFiles[parts[len(parts)-1]]
}

// soleEditPath is the edit/write call's path when exactly one argument key
// names it. The tools decode their arguments case-insensitively, so a
// "PATH" key reaches the tool while PathArgOf, which matches keys exactly,
// does not see it; any such spelling, or two path keys, is ambiguous, and
// auto mode then classifies instead of fast-pathing.
func soleEditPath(args map[string]any) (string, bool) {
	var found string
	n := 0
	for k, v := range args {
		switch strings.ToLower(k) {
		case "path", "file_path", "filepath":
			n++
			s, ok := v.(string)
			if !ok || (k != "path" && k != "file_path" && k != "filePath") {
				return "", false
			}
			found = s
		}
	}
	return found, n == 1 && found != ""
}
