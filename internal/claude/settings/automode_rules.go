package settings

import (
	"path"
	"strings"
)

// Broad allow rules in auto mode. Claude Code sets aside, for as long as
// auto mode is on, allow rules that would let arbitrary code run without
// the classifier seeing it: a blanket Bash rule, interpreters and shells
// under a wildcard, package-manager run commands, and subagent (Agent)
// rules. Narrow rules such as Bash(npm test) stay. Leaving auto mode puts
// them back (code.claude.com/docs/en/permission-modes, "How the classifier
// evaluates actions"; code.claude.com/docs/en/auto-mode-config, "Route all
// shell commands through the classifier"). Deny and ask rules are never
// touched.

// codeRunners run whatever code their arguments name: shells,
// interpreters, one-shot package runners, and wrappers that run another
// command. The rule is broad when the program is the whole rule or is
// followed by a wildcard.
var codeRunners = map[string]bool{
	// shells
	"bash": true, "sh": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "pwsh": true,
	// interpreters (a versioned name, python3.12, counts as its family)
	"python": true, "pypy": true, "node": true, "deno": true, "bun": true, "tsx": true, "ts-node": true,
	"ruby": true, "perl": true, "php": true, "lua": true, "osascript": true,
	// one-shot package runners
	"npx": true, "bunx": true, "pnpx": true, "uvx": true,
	// wrappers that run the command they are given
	"eval": true, "exec": true, "env": true, "xargs": true, "sudo": true, "ssh": true,
	"nohup": true, "timeout": true, "nice": true, "command": true, "watch": true,
}

// toolchains are package managers and build tools whose subcommands run
// project scripts or arbitrary programs (npm run/exec, git aliases and -c,
// go test -exec, make targets): any wildcard after one is broad. The docs
// name package-manager run commands; git, go and the build tools are
// kiln's own extension of the same reasoning.
var toolchains = map[string]bool{
	"npm": true, "pnpm": true, "yarn": true, "bun": true, "deno": true,
	"pip": true, "pipx": true, "uv": true, "poetry": true, "cargo": true, "go": true, "git": true,
	"make": true, "gradle": true, "gradlew": true, "mvn": true, "mvnw": true, "dotnet": true,
	"bundle": true, "rake": true, "composer": true, "gem": true, "mix": true, "swift": true,
}

// programFamily is a command word's program: the base name, without a
// version suffix ("/usr/bin/python3.12" → "python", "./gradlew" → "gradlew").
func programFamily(word string) string {
	base := path.Base(strings.ReplaceAll(word, `\`, "/"))
	if trimmed := strings.TrimRight(base, "0123456789."); trimmed != "" && trimmed != base {
		if codeRunners[trimmed] || toolchains[trimmed] {
			return trimmed
		}
	}
	return base
}

// IsBroadAutoModeAllow reports an allow rule auto mode sets aside.
func IsBroadAutoModeAllow(rule string) bool {
	tool, content, hasContent := strings.TrimSpace(rule), "", false
	if m := parenRule.FindStringSubmatch(tool); m != nil {
		tool, content, hasContent = strings.TrimSpace(m[1]), m[2], true
	}
	switch normTool(tool) {
	case "task", "agent":
		// A subagent dispatch must reach the classifier, which judges the
		// delegated task.
		return true
	case "bash", "bashbackground":
	default:
		return false
	}
	c := strings.ToLower(strings.TrimSpace(content))
	if !hasContent || c == "" || strings.Trim(c, "* ") == "" {
		return true // Bash, Bash(), Bash(*)
	}
	if strings.HasSuffix(c, ":*") {
		c = strings.TrimSuffix(c, ":*") + " *"
	}
	first, rest, _ := strings.Cut(c, " ")
	wild := strings.Contains(c, "*")
	// A wildcard in the program itself (python*, *, py*) can name any
	// interpreter.
	if strings.Contains(first, "*") {
		return true
	}
	fam := programFamily(first)
	switch {
	case codeRunners[fam]:
		// The program alone, or any wildcard after it (python *, sh -c *).
		return rest == "" || wild
	case toolchains[fam]:
		return wild
	}
	return false
}

// WithoutBroadAutoModeAllows is p without the allow rules auto mode sets
// aside, and those rules. The From list stays aligned with Allow.
func WithoutBroadAutoModeAllows(p Permissions) (Permissions, []string) {
	var aside []string
	allow := make([]string, 0, len(p.Allow))
	var from []RuleSource
	for i, r := range p.Allow {
		if IsBroadAutoModeAllow(r) {
			aside = append(aside, r)
			continue
		}
		allow = append(allow, r)
		if i < len(p.AllowFrom) {
			from = append(from, p.AllowFrom[i])
		}
	}
	if len(aside) == 0 {
		return p, nil
	}
	p.Allow, p.AllowFrom = allow, from
	return p, aside
}
