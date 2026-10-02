package settings

import "strings"

// Broad allow rules in auto mode. Claude Code sets aside, for as long as
// auto mode is on, allow rules that would let arbitrary code run without
// the classifier seeing it: a blanket Bash rule, interpreters and shells
// under a wildcard, package-manager run commands, and subagent (Agent)
// rules. Narrow rules such as Bash(npm test) stay. Leaving auto mode puts
// them back (code.claude.com/docs/en/permission-modes, "How the classifier
// evaluates actions"; code.claude.com/docs/en/auto-mode-config, "Route all
// shell commands through the classifier"). Deny and ask rules are never
// touched.

// codeRunners are command prefixes that run whatever code their arguments
// name: shells, interpreters, package runners, and wrappers that run
// another command. A wildcard after one of them allows arbitrary code.
var codeRunners = []string{
	// shells
	"bash", "sh", "zsh", "fish", "dash", "ksh", "pwsh",
	// interpreters
	"python", "python2", "python3", "node", "deno", "bun", "tsx", "ts-node",
	"ruby", "perl", "php", "lua", "osascript",
	// package-manager and project runners
	"npx", "bunx", "pnpx", "uvx",
	"npm run", "npm exec", "yarn run", "yarn exec", "yarn dlx",
	"pnpm run", "pnpm exec", "pnpm dlx", "bun run", "bun x",
	"uv run", "poetry run", "pipx run", "cargo run", "go run",
	// wrappers that run the command they are given
	"eval", "exec", "env", "xargs", "sudo", "ssh", "nohup", "timeout", "nice", "command", "watch",
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
	for _, r := range codeRunners {
		if !strings.HasPrefix(c, r) {
			continue
		}
		rest := c[len(r):]
		switch {
		case rest == "", strings.HasPrefix(rest, "*"), rest == " *":
			return true
		case strings.HasPrefix(rest, " -") && strings.HasSuffix(rest, "*"):
			return true // python -c *, sh -c *
		}
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
