package settings

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// BashSegments splits a bash command line into its top-level commands, on
// &&, ||, ;, |, |&, a backgrounding & and newlines, as the parser reads
// them (bash_parse.go). Each segment is the command's text as written,
// redirections included ("go test 2>&1" is one segment).
//
// opaque reports a construct whose effect no prefix rule can see: command
// substitution ($(...) or backticks) or process substitution (<(...),
// >(...)). A line kiln cannot parse is one opaque segment.
func BashSegments(cmd string) (segments []string, opaque bool) {
	f, src, ok := parseBash(cmd)
	if !ok {
		if t := strings.TrimSpace(cmd); t != "" {
			return []string{t}, true
		}
		return nil, true
	}
	var add func(s *syntax.Stmt)
	add = func(s *syntax.Stmt) {
		if s.Cmd == nil {
			return
		}
		if b, ok := s.Cmd.(*syntax.BinaryCmd); ok && len(s.Redirs) == 0 {
			add(b.X)
			add(b.Y)
			return
		}
		start, end := s.Cmd.Pos().Offset(), s.Cmd.End().Offset()
		for _, r := range s.Redirs {
			start = min(start, r.Pos().Offset())
			end = max(end, r.End().Offset())
		}
		if t := strings.TrimSpace(src[start:end]); t != "" {
			segments = append(segments, t)
		}
	}
	for _, s := range f.Stmts {
		add(s)
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			opaque = true
		}
		return !opaque
	})
	return segments, opaque
}

// IsBashTool reports the tools Bash rules govern: bash, and bash_background
// (kiln's background form of the same tool).
func IsBashTool(toolName string) bool { return isBashTool(toolName) }

func isBashTool(toolName string) bool {
	return strings.EqualFold(toolName, "bash") || strings.EqualFold(toolName, "bash_background")
}

// matchesBashRule matches a Bash rule against one command text, for either
// bash tool (a rule naming bash_background itself also applies to it).
func matchesBashRule(rule, toolName, text string) bool {
	return MatchesRule(rule, "bash", text) || (!strings.EqualFold(toolName, "bash") && MatchesRule(rule, toolName, text))
}

// isExactBashRule reports a Bash rule with no wildcard: the only kind that
// approves an exec wrapper or a find that runs or deletes.
func isExactBashRule(rule string) bool {
	m := parenRule.FindStringSubmatch(rule)
	return m != nil && !strings.Contains(m[2], "*")
}

// isWildcardBashRule reports a rule approving every bash command: a bare
// Bash, or Bash(*).
func isWildcardBashRule(rule, toolName string) bool {
	if m := parenRule.FindStringSubmatch(rule); m != nil {
		return strings.TrimSpace(m[2]) == "*" && matchesBashRule(rule, toolName, "")
	}
	return matchesBashRule(rule, toolName, "")
}

// bashGuarded reports a Bash deny or ask rule: then a command kiln cannot
// fully see is asked about rather than allowed past it.
func bashGuarded(p Permissions, toolName string) bool {
	for _, list := range [][]string{p.Deny, p.Ask} {
		for _, r := range list {
			if _, isPath := splitFileRule(r); isPath {
				continue
			}
			name := r
			if m := parenRule.FindStringSubmatch(r); m != nil {
				name = m[1]
			}
			name = strings.ToLower(name)
			if sameTool(name, "bash") || sameTool(name, strings.ToLower(toolName)) {
				return true
			}
		}
	}
	return false
}

// roughSegments splits a line kiln could not parse on every byte that
// could separate commands, so a deny rule still sees what it can.
func roughSegments(cmd string) []string {
	var out []string
	for _, s := range strings.FieldsFunc(cmd, func(r rune) bool {
		return strings.ContainsRune("\n\r;&|()`{}", r)
	}) {
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// bashRuleVerdicts evaluates Bash rules on the commands the line runs
// (Claude Code's "Compound commands"):
//   - a deny or ask rule applies when it matches ANY command, nested ones
//     included (a substitution, a subshell, a loop body, "sh -c"), as
//     written or with its leading assignments and wrappers stripped, or
//     the whole line;
//   - an allow rule approves the line only when EVERY command is covered —
//     matched by an allow rule, or read-only and local (readonly_bash.go)
//     — at least one by a rule, and nothing runs that kiln cannot name. A
//     line kiln cannot parse is never approved; only a bare Bash (or
//     Bash(*)) approves a command it cannot name.
func bashRuleVerdicts(permissions Permissions, a *bashAnalysis, toolName, cmd string) (deny, ask, allow bool) {
	forms := []string{cmd}
	if a.parsed {
		for _, c := range a.cmds {
			forms = append(forms, c.forms...)
		}
		forms = append(forms, a.docLines...)
	} else {
		forms = append(forms, roughSegments(cmd)...)
	}
	seen := make(map[string]bool, len(forms))
	unique := forms[:0]
	for _, f := range forms {
		if !seen[f] {
			seen[f] = true
			unique = append(unique, f)
		}
	}
	forms = unique
	anyMatch := func(rules []string) bool {
		for _, r := range rules {
			if _, isPath := splitFileRule(r); isPath {
				continue
			}
			for _, f := range forms {
				if matchesBashRule(r, toolName, f) {
					return true
				}
			}
		}
		return false
	}
	deny = anyMatch(permissions.Deny)
	ask = anyMatch(permissions.Ask)
	if !a.parsed || len(a.cmds) == 0 || a.outsideWrite {
		// An output redirect outside the working directory (or one that
		// expands) needs approval whatever rule allows the command.
		return deny, ask, false
	}
	for _, r := range permissions.Allow {
		if _, isPath := splitFileRule(r); !isPath && isWildcardBashRule(r, toolName) {
			return deny, ask, true
		}
	}
	if a.unknown {
		return deny, ask, false
	}
	byRule := false
	roShape := a.roShape() && !a.nonLocalRedirect
	for _, c := range a.cmds {
		if allowedByRule(permissions.Allow, c, toolName) {
			byRule = true
			continue
		}
		if !(c.readOnly && c.local && roShape) {
			return deny, ask, false
		}
	}
	return deny, ask, byRule
}

// allowedByRule reports one command an allow rule approves: an exec
// wrapper (exactOnly) only by an exact rule.
func allowedByRule(allow []string, c bashCmd, toolName string) bool {
	for _, r := range allow {
		if _, isPath := splitFileRule(r); isPath {
			continue
		}
		if c.exactOnly && !isExactBashRule(r) {
			continue
		}
		for _, text := range []string{c.allow, c.allowRaw, c.allowFull, c.allowFullRaw} {
			if text != "" && matchesBashRule(r, toolName, text) {
				return true
			}
		}
	}
	return false
}
