package settings

import (
	"regexp"
	"strings"
)

// IsReadOnlyCommand reports whether a bash command line only reads. Plan
// mode allows exactly these, so the model can look around while planning
// (cat the spec, ls the tree, git log) without being able to change
// anything.
//
// It is an allowlist, and conservative by construction: anything it does
// not fully understand is not read-only. A command line is read-only when
// it is a chain (&&, ||, ;) of pipelines (|) whose every command is a
// known read-only program used in a read-only way, with no output
// redirection except to /dev/null, and no command or process
// substitution, variable expansion, backgrounding or escapes that could
// hide what actually runs.
func IsReadOnlyCommand(cmd string) bool {
	segments, ok := splitCommandLine(cmd)
	if !ok || len(segments) == 0 {
		return false
	}
	for _, words := range segments {
		if !readOnlyInvocation(words) {
			return false
		}
	}
	return true
}

// splitCommandLine tokenizes cmd into the word lists of its individual
// commands, splitting on &&, ||, ; and |. Quotes group words; the quotes
// themselves are dropped. It fails (ok=false) on every construct the
// classifier refuses to reason about.
func splitCommandLine(cmd string) (segments [][]string, ok bool) {
	var words []string
	var cur strings.Builder
	inWord := false
	flushWord := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	flushSegment := func() bool {
		flushWord()
		if len(words) == 0 {
			return false // "a && && b", a leading "|", and the like
		}
		segments = append(segments, words)
		words = nil
		return true
	}

	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch c {
		case '\'':
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			cur.WriteString(cmd[i+1 : i+1+end])
			inWord = true
			i += end + 1
		case '"':
			end := strings.IndexByte(cmd[i+1:], '"')
			if end < 0 {
				return nil, false
			}
			body := cmd[i+1 : i+1+end]
			// Double quotes still expand $ and backticks.
			if strings.ContainsAny(body, "$`\\") {
				return nil, false
			}
			cur.WriteString(body)
			inWord = true
			i += end + 1
		case ' ', '\t':
			flushWord()
		case '&':
			if strings.HasPrefix(cmd[i:], "&&") {
				if !flushSegment() {
					return nil, false
				}
				i++
				continue
			}
			if strings.HasPrefix(cmd[i:], "&>/dev/null") {
				flushWord()
				i += len("&>/dev/null") - 1
				continue
			}
			return nil, false // backgrounding
		case '|':
			if strings.HasPrefix(cmd[i:], "||") {
				i++
			}
			if !flushSegment() {
				return nil, false
			}
		case ';':
			if !flushSegment() {
				return nil, false
			}
		case '>', '<':
			n, redirectOK := devNullRedirect(cmd, i, inWord, cur.String())
			if !redirectOK {
				return nil, false
			}
			// A leading fd digit ("2" of "2>") was taken as a word
			// character; drop it.
			if inWord && (cur.String() == "1" || cur.String() == "2") {
				cur.Reset()
				inWord = false
			}
			flushWord()
			i += n - 1
		case '`', '$', '\\', '(', ')', '{', '}', '\n', '\r':
			return nil, false
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if !flushSegment() {
		return nil, false
	}
	return segments, true
}

// devNullRedirect accepts only redirections that cannot write anywhere
// real: ">/dev/null", "2>/dev/null", "2>&1", "1>&2" and an input redirect
// "< file" (reading). It returns how many bytes of cmd, starting at i,
// the operator spans (the target file of "<" is left as the next word).
func devNullRedirect(cmd string, i int, inWord bool, word string) (int, bool) {
	if inWord && word != "1" && word != "2" {
		return 0, false // "file>out": a redirect glued to a word
	}
	rest := cmd[i:]
	switch {
	case strings.HasPrefix(rest, ">/dev/null"):
		return len(">/dev/null"), true
	case strings.HasPrefix(rest, "> /dev/null"):
		return len("> /dev/null"), true
	case strings.HasPrefix(rest, ">&1"), strings.HasPrefix(rest, ">&2"):
		return len(">&1"), true
	case strings.HasPrefix(rest, "<") && !strings.HasPrefix(rest, "<<") && !strings.HasPrefix(rest, "<("):
		return 1, !inWord
	}
	return 0, false
}

// readOnlyPrograms maps each allowed program to a check of its arguments.
// A nil check means every argument is fine.
var readOnlyPrograms = map[string]func(args []string) bool{
	"cat": nil, "head": nil, "tail": nil, "ls": nil, "pwd": nil, "echo": nil,
	"printf": nil, "wc": nil, "cut": nil, "tr": nil, "grep": nil, "egrep": nil,
	"fgrep": nil, "file": nil, "stat": nil, "du": nil, "df": nil, "which": nil,
	"whoami": nil, "uname": nil, "basename": nil, "dirname": nil,
	"realpath": nil, "readlink": nil, "diff": nil, "cmp": nil, "comm": nil,
	"jq": nil, "true": nil, "false": nil, "test": nil, "[": nil, "cd": nil,
	"printenv": nil, "nl": nil, "column": nil,
	"sort": func(a []string) bool { return !hasArg(a, "-o", "--output") },
	"uniq": func(a []string) bool { return len(positional(a)) <= 1 }, // a 2nd operand is an output file
	"tree": func(a []string) bool { return !hasArg(a, "-o") },
	"rg":   func(a []string) bool { return !hasArg(a, "--pre") },
	"date": func(a []string) bool { return !hasArg(a, "-s", "--set") },
	"find": func(a []string) bool {
		return !hasArg(a, "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls")
	},
	"env": func(a []string) bool { return len(a) == 0 }, // "env cmd" runs cmd
	"sed": readOnlySed,
	"git": readOnlyGit,
	"go":  readOnlyGo,
	"node": func(a []string) bool {
		return len(a) == 1 && (a[0] == "--version" || a[0] == "-v")
	},
	"npm": func(a []string) bool {
		return len(a) >= 1 && (a[0] == "--version" || a[0] == "-v" || a[0] == "ls" || a[0] == "list" || a[0] == "view")
	},
}

// wrappers run their argument list as a command; the command is judged,
// not the wrapper. "rtk" is the token-filtering proxy some setups rewrite
// every bash call through (via a PreToolUse hook, so the gate sees the
// rewritten line); its "read" is cat, "proxy" runs the rest verbatim, and
// its other read-side subcommands share their underlying tool's name.
func unwrap(words []string) ([]string, bool) {
	if len(words) >= 2 && words[0] == "rtk" {
		switch words[1] {
		case "read":
			return append([]string{"cat"}, words[2:]...), true
		case "proxy":
			return words[2:], len(words) > 2
		case "ls", "grep", "find", "tree", "git", "wc", "diff", "cat", "head", "tail", "rg":
			return words[1:], true
		}
		return nil, false
	}
	return words, true
}

func readOnlyInvocation(words []string) bool {
	words, ok := unwrap(words)
	if !ok || len(words) == 0 {
		return false
	}
	name := words[0]
	if strings.Contains(name, "=") || strings.Contains(name, "/") {
		return false // "FOO=x cmd" or "./script": not a known program
	}
	check, known := readOnlyPrograms[name]
	if !known {
		return false
	}
	return check == nil || check(words[1:])
}

// sedPrintScript is the one sed shape allowed: print a line range.
var sedPrintScript = regexp.MustCompile(`^([0-9]+|\$)(,([0-9]+|\$))?p$`)

func readOnlySed(a []string) bool {
	quiet, script := false, ""
	for _, w := range a {
		switch {
		case w == "-n":
			quiet = true
		case strings.HasPrefix(w, "-"):
			return false // -i, -e, -f, --in-place, ...
		case script == "":
			script = w
		}
	}
	return quiet && sedPrintScript.MatchString(script)
}

// readOnlyGitSubcommands lists git subcommands that only read, each with
// an optional argument check.
var readOnlyGitSubcommands = map[string]func(args []string) bool{
	"status": nil, "log": nil, "diff": nil, "show": nil, "rev-parse": nil,
	"ls-files": nil, "blame": nil, "describe": nil, "shortlog": nil,
	"grep": nil, "rev-list": nil, "ls-tree": nil, "cat-file": nil,
	"branch": func(a []string) bool {
		for _, w := range a {
			if w != "-a" && w != "-r" && w != "-v" && w != "-vv" && w != "--list" && w != "--show-current" && w != "--all" {
				return false // -d, -m, a new branch name, ...
			}
		}
		return true
	},
	"remote": func(a []string) bool { return len(a) == 0 || (len(a) == 1 && a[0] == "-v") },
	"tag":    func(a []string) bool { return len(a) == 0 || (len(a) >= 1 && a[0] == "-l") },
	"config": func(a []string) bool {
		return len(a) >= 1 && (a[0] == "--get" || a[0] == "--list" || a[0] == "-l" || a[0] == "--get-all")
	},
}

func readOnlyGit(a []string) bool {
	if len(a) == 0 {
		return false
	}
	// Global options before the subcommand (-c, -C, --git-dir, ...) can
	// point git at other config, including ones that run programs.
	sub := a[0]
	check, known := readOnlyGitSubcommands[sub]
	if !known {
		return false
	}
	rest := a[1:]
	for _, w := range rest {
		if strings.HasPrefix(w, "--output") || w == "--ext-diff" || w == "--textconv" || strings.HasPrefix(w, "--exec") {
			return false // writes a file, or runs a configured program
		}
	}
	return check == nil || check(rest)
}

func readOnlyGo(a []string) bool {
	if len(a) == 0 {
		return false
	}
	switch a[0] {
	case "version", "list", "doc":
		return true
	case "env":
		return !hasArg(a[1:], "-w", "-u")
	}
	return false
}

// hasArg reports whether any of flags appears in args, as the whole word
// or as "flag=value".
func hasArg(args []string, flags ...string) bool {
	for _, w := range args {
		for _, f := range flags {
			if w == f || strings.HasPrefix(w, f+"=") {
				return true
			}
		}
	}
	return false
}

// positional returns the arguments that are not flags.
func positional(args []string) []string {
	var out []string
	for _, w := range args {
		if !strings.HasPrefix(w, "-") {
			out = append(out, w)
		}
	}
	return out
}
