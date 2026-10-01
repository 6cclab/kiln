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

// BashDontAskRule is the rule a bash "don't ask again" grants, and the text
// the prompt shows for it: the first two words of the command plus " *"
// (Bash(<prefix> *) semantics, e.g. "openssl rand -hex 4" -> "openssl rand
// *"). For a command line it is taken from the first segment that is not a
// cd, so "cd api && npm test" grants "npm test *", not a rule tied to one
// directory. The rule is matched per command (see Decide), so it never
// covers the other commands of a line it did not name.
func BashDontAskRule(command string) string {
	segments, _ := BashSegments(command)
	first := command
	for _, s := range segments {
		if f := strings.Fields(s); len(f) > 0 && f[0] != "cd" {
			first = s
			break
		}
	}
	fields := strings.Fields(first)
	switch len(fields) {
	case 0:
		return "*"
	case 1:
		return fields[0] + " *"
	}
	return fields[0] + " " + fields[1] + " *"
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
		return strings.ContainsRune("\n;&|()`{}", r)
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
	if !a.parsed || len(a.cmds) == 0 {
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
		ok := false
		for _, r := range permissions.Allow {
			if _, isPath := splitFileRule(r); isPath {
				continue
			}
			if c.exactOnly && !isExactBashRule(r) {
				continue
			}
			if matchesBashRule(r, toolName, c.allow) || (c.allowRaw != "" && matchesBashRule(r, toolName, c.allowRaw)) {
				ok = true
				break
			}
		}
		if ok {
			byRule = true
			continue
		}
		if !(c.readOnly && c.local && roShape) {
			return deny, ask, false
		}
	}
	return deny, ask, byRule
}
