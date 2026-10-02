package permission

import (
	"path/filepath"
	"strings"

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
}

// protectedPath reports a path, as written or as it resolves through
// symlinks, that is or lies under a protected directory, or names a
// protected file. .claude/worktrees is exempt, as in Claude Code.
func (g *Gate) protectedPath(path string) bool {
	base := "."
	if len(g.roots) > 0 {
		base = g.roots[0]
	}
	full := execenv.ResolveToolPath(base, path)
	candidates := []string{full}
	if real, ok := execenv.RealPath(full); ok {
		candidates = append(candidates, real)
	} else {
		return true // unresolvable: do not wave it through
	}
	for _, p := range candidates {
		parts := strings.Split(filepath.ToSlash(filepath.Clean(p)), "/")
		for i, part := range parts {
			part = strings.ToLower(part)
			if protectedDirs[part] && !(part == ".claude" && i+1 < len(parts) && strings.EqualFold(parts[i+1], "worktrees")) {
				return true
			}
			if part == ".config" && i+1 < len(parts) && strings.EqualFold(parts[i+1], "git") {
				return true
			}
		}
		if protectedFiles[strings.ToLower(parts[len(parts)-1])] {
			return true
		}
	}
	return false
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
