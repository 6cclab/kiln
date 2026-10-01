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
// not fully understand is not read-only. The line is parsed (bash_parse.go)
// and is read-only when it is a list (&&, ||, ;, newlines) of pipelines (|)
// whose every command is a known read-only program used in a read-only way
// (readOnlyInvocation), every word literal, with no output redirection
// except to /dev/null and no substitution, assignment, subshell, group,
// background job or heredoc. As in Claude Code ("Read-only commands"), an
// unquoted glob given to a command with write- or exec-capable flags
// (find, sort, sed, git) is not, nor is git after a cd to another
// directory, nor a relative redirect target after a cd.
func IsReadOnlyCommand(cmd string) bool {
	return analyzeBash(cmd, "", "", false).readOnly()
}

// readOnly is IsReadOnlyCommand on an analysis.
func (a *bashAnalysis) readOnly() bool {
	if !a.parsed || a.unknown || len(a.cmds) == 0 || !a.roShape() {
		return false
	}
	for _, c := range a.cmds {
		if !c.readOnly {
			return false
		}
	}
	return true
}

// roShape reports a line whose structure a read-only line may have.
func (a *bashAnalysis) roShape() bool {
	return a.roOK && !(a.git && a.cdAway) && !(a.sawCd && a.relRedirect)
}

// CommandWords is every word of a command line (quotes removed, operators
// dropped, redirect targets included), for a caller that must check the
// paths a read-only command touches; ok is false when it cannot be parsed
// or a word is not literal.
func CommandWords(cmd string) (words []string, ok bool) {
	a := analyzeBash(cmd, "", "", false)
	if !a.parsed || !a.literalWords {
		return nil, false
	}
	return a.words, true
}

// readOnlyPrograms maps each allowed program to a check of its arguments.
// A nil check means every argument is fine.
var readOnlyPrograms = map[string]func(args []string) bool{
	"cat": nil, "head": nil, "tail": nil, "ls": nil, "pwd": nil, "echo": nil,
	"wc": nil, "cut": nil, "tr": nil, "grep": nil, "egrep": nil,
	"fgrep": nil, "stat": nil, "du": nil, "df": nil, "which": nil,
	"whoami": nil, "uname": nil, "basename": nil, "dirname": nil,
	"realpath": nil, "readlink": nil, "diff": nil, "cmp": nil, "comm": nil,
	"jq": nil, "true": nil, "false": nil, "test": nil, "[": nil, "cd": nil,
	"printenv": nil, "nl": nil, "column": nil,
	// Each check below refuses the options that write a file or run a
	// program, in every spelling getopt accepts (optionSet).
	"printf": func(a []string) bool { return !optionSet(a, "v") }, // -v assigns a variable (and its subscript runs)
	"file": func(a []string) bool {
		return !optionSet(a, "Cmf", "compile", "magic-file", "files-from") // -C writes; -m, -f open the paths they name
	},
	"sort": func(a []string) bool { return !optionSet(a, "o", "output", "compress-program") },
	"uniq": func(a []string) bool { return len(positional(a)) <= 1 }, // a 2nd operand is an output file
	"tree": func(a []string) bool {
		return !optionSet(a, "o") && !(optionSet(a, "R") && optionSet(a, "H")) // -o writes; -R with -H writes an index per directory
	},
	"rg":   func(a []string) bool { return !optionSet(a, "", "pre", "hostname-bin") },
	"date": func(a []string) bool { return !optionSet(a, "s", "set") },
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

// rtkUnwrap judges the command rtk runs, not rtk. rtk is the
// token-filtering proxy some setups rewrite every bash call through (via a
// PreToolUse hook, so the gate sees the rewritten line); its "read" is cat,
// "proxy" runs the rest verbatim, and its other read-side subcommands
// share their underlying tool's name.
func rtkUnwrap(words []string) ([]string, bool) {
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
	words, ok := rtkUnwrap(words)
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
	// Writes a file, or runs a configured or given program.
	if optionSet(rest, "", "output", "ext-diff", "textconv", "exec", "filters", "open-files-in-pager") {
		return false
	}
	if sub == "grep" && optionSet(rest, "O") {
		return false // -O opens the matching files in a pager
	}
	return check == nil || check(rest)
}

// optionSet reports an argument that sets one of the short options in
// short — alone ("-o"), in a cluster ("-mo") or with its value attached
// ("-oFILE") — or one of the long options, spelt out or abbreviated as
// GNU getopt accepts ("--outp=x" for --output). Arguments after "--" are
// operands. It errs towards a match: a cluster's later letters may be an
// earlier option's value.
func optionSet(args []string, short string, long ...string) bool {
	for _, w := range args {
		switch {
		case w == "--":
			return false
		case strings.HasPrefix(w, "--"):
			name, _, _ := strings.Cut(w[2:], "=")
			for _, l := range long {
				if name != "" && strings.HasPrefix(l, name) {
					return true
				}
			}
		case len(w) > 1 && w[0] == '-' && short != "" && strings.ContainsAny(w[1:], short):
			return true
		}
	}
	return false
}

func readOnlyGo(a []string) bool {
	if len(a) == 0 {
		return false
	}
	for _, w := range a[1:] {
		// -toolexec and -exec run a program for each build step or binary.
		name, _, _ := strings.Cut(strings.TrimLeft(w, "-"), "=")
		if strings.HasPrefix(w, "-") && (name == "toolexec" || name == "exec") {
			return false
		}
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
