package settings

import "strings"

// BashSegments splits a bash command line into its individual commands, on
// &&, ||, ;, |, a backgrounding & and newlines, honouring quotes.
// Redirections stay part of their command ("go test 2>&1" is one segment).
//
// opaque reports a construct whose effect no prefix rule can see: command
// substitution ($(...) or backticks) or process substitution (<(...),
// >(...)). A rule matching the visible text says nothing about what those
// run, so they never count as allowed by a rule.
func BashSegments(cmd string) (segments []string, opaque bool) {
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segments = append(segments, s)
		}
		cur.Reset()
	}
	inComment := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if inComment {
			// A comment runs to the newline: quotes and backslashes in it
			// are text (so "# don't" opens no quote and "# \" continues
			// no line). Separators still split, which only ever makes a
			// line need more allow rules, never fewer.
			switch c {
			case '\n':
				inComment = false
				flush()
			case ';', '|', '&':
				flush()
			default:
				cur.WriteByte(c)
			}
			continue
		}
		switch {
		case c == '#' && (i == 0 || strings.IndexByte(shellMeta, cmd[i-1]) >= 0):
			inComment = true
			cur.WriteByte(c)
		case c == '$' && i+1 < len(cmd) && cmd[i+1] == '\'':
			// $'…': a backslash escapes the next byte, so \' does not
			// end it (and a ";" inside is text, not a separator).
			end, _ := ansiEnd(cmd, i+2)
			cur.WriteString(cmd[i:end])
			i = end - 1
		case c == '\'':
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				cur.WriteString(cmd[i:])
				i = len(cmd)
				continue
			}
			cur.WriteString(cmd[i : i+end+2])
			i += end + 1
		case c == '"':
			end := i + 1
			for end < len(cmd) && cmd[end] != '"' {
				if cmd[end] == '\\' {
					end++
				}
				end++
			}
			body := cmd[i:min(end+1, len(cmd))]
			if strings.Contains(body, "$(") || strings.Contains(body, "`") {
				opaque = true
			}
			cur.WriteString(body)
			i = end
		case c == '\\' && i+1 < len(cmd):
			cur.WriteString(cmd[i : i+2])
			i++
		case c == '`', c == '$' && strings.HasPrefix(cmd[i:], "$("),
			(c == '<' || c == '>') && strings.HasPrefix(cmd[i+1:], "("):
			opaque = true
			cur.WriteByte(c)
		case c == '&' && strings.HasPrefix(cmd[i:], "&&"),
			c == '|' && strings.HasPrefix(cmd[i:], "||"):
			flush()
			i++
		case c == '&':
			// "&>" and ">&" / "2>&1" are redirections, not backgrounding.
			if strings.HasPrefix(cmd[i:], "&>") || (i > 0 && cmd[i-1] == '>') {
				cur.WriteByte(c)
				continue
			}
			flush()
		case c == '|' && i > 0 && cmd[i-1] == '>':
			// ">|" (and "2>|") is a clobbering output redirection, not a
			// pipe: it stays part of its command.
			cur.WriteByte(c)
		case c == ';', c == '|', c == '\n':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return segments, opaque
}

// BashDontAskRule is the rule a bash "don't ask again" grants, and the text
// the prompt shows for it: the first two words of the command plus " *"
// (Bash(<prefix> *) semantics, e.g. "openssl rand -hex 4" -> "openssl rand
// *"). For a command line it is taken from the first segment that is not a
// cd, so "cd api && npm test" grants "npm test *", not a rule tied to one
// directory. The rule is matched per segment (see Decide), so it never
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

// bashRuleVerdicts evaluates bash rules segment by segment, the way a
// command line actually runs:
//   - a deny or ask rule applies when it matches ANY segment (or the whole
//     line), so "ls && rm -rf x" is caught by Bash(rm *);
//   - an allow rule applies only when EVERY segment is matched by some
//     allow rule and nothing is opaque, so Bash(git *) no longer approves
//     "git status && rm -rf ~".
func bashRuleVerdicts(permissions Permissions, toolName, cmd string) (deny, ask, allow bool) {
	// bash deletes backslash-newline before splitting words: "rm \<NL>-rf
	// x" is "rm -rf x", and Bash(rm *) must see it so.
	cmd = joinContinuations(cmd)
	segments, opaque := BashSegments(cmd)
	anyMatch := func(rules []string) bool {
		for _, r := range rules {
			if MatchesRule(r, toolName, cmd) {
				return true
			}
			for _, s := range segments {
				if MatchesRule(r, toolName, s) {
					return true
				}
			}
		}
		return false
	}
	deny = anyMatch(permissions.Deny)
	ask = anyMatch(permissions.Ask)
	if opaque || len(segments) == 0 {
		return deny, ask, false
	}
	allow = true
	for _, s := range segments {
		matched := false
		for _, r := range permissions.Allow {
			if MatchesRule(r, toolName, s) {
				matched = true
				break
			}
		}
		if !matched {
			allow = false
			break
		}
	}
	return deny, ask, allow
}
