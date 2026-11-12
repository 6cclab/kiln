package tools

import (
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Command shapes read from a bash command's syntax alone. They only steer
// what kiln records about a command (evidence, outcome counters); they never
// decide whether it runs. The text inside a nested `sh -c` string, scripts an
// interpreter reads, and command substitutions are not inspected.

// UnparseableCommand is CommandMutatesFiles's reason for a command the shell
// parser rejects: nothing is known about what it writes.
const UnparseableCommand = "unparseable shell syntax"

func parseCommand(command string) *syntax.File {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	return file
}

// literalWords returns a call's words, with "" for any word that is not a
// plain literal (an expansion, quoting, a substitution).
func literalWords(call *syntax.CallExpr) []string {
	words := make([]string, len(call.Args))
	for i, w := range call.Args {
		if lit := w.Lit(); lit != "" {
			words[i] = lit
			continue
		}
		var b strings.Builder
		literal := true
		for _, part := range w.Parts {
			switch p := part.(type) {
			case *syntax.Lit:
				b.WriteString(p.Value)
			case *syntax.SglQuoted:
				b.WriteString(p.Value)
			default:
				literal = false
			}
		}
		if literal {
			words[i] = b.String()
		}
	}
	return words
}

// mutatingGitCommands rewrite the working tree or index.
var mutatingGitCommands = map[string]bool{
	"am": true, "apply": true, "checkout": true, "cherry-pick": true, "clean": true, "merge": true,
	"mv": true, "pull": true, "rebase": true, "reset": true, "restore": true, "revert": true,
	"rm": true, "stash": true, "switch": true,
}

// fileWritingCommands write the files they are given.
var fileWritingCommands = map[string]bool{
	"cp": true, "dd": true, "install": true, "ln": true, "mv": true, "patch": true, "rm": true,
	"tee": true, "touch": true, "truncate": true,
}

// shortFlagsContain reports whether word is a cluster of short options
// ("-i", "-pi", "-0pi") that includes flag.
func shortFlagsContain(word string, flag byte) bool {
	return len(word) > 1 && word[0] == '-' && word[1] != '-' && strings.IndexByte(word[1:], flag) >= 0
}

// scratchTarget reports a redirect target that is not a project file: a
// device or the system temporary directory.
func scratchTarget(target string) bool {
	return strings.HasPrefix(target, "/dev/") || strings.HasPrefix(target, "/tmp/") || strings.HasPrefix(target, "/private/tmp/")
}

// CommandMutatesFiles names how command writes files as it runs (an in-place
// edit, a copy, a git restore, an output redirect to a project file), or
// returns "" when its syntax shows no such write. An unparseable command
// returns "unparseable shell syntax".
func CommandMutatesFiles(command string) string {
	file := parseCommand(command)
	if file == nil {
		return UnparseableCommand
	}
	reason := ""
	syntax.Walk(file, func(node syntax.Node) bool {
		if reason != "" {
			return false
		}
		switch n := node.(type) {
		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob:
				target := ""
				if n.Word != nil {
					target = literalWords(&syntax.CallExpr{Args: []*syntax.Word{n.Word}})[0]
				}
				if !scratchTarget(target) {
					reason = "output redirect to a file"
				}
			}
		case *syntax.CallExpr:
			words := literalWords(n)
			if len(words) == 0 || words[0] == "" {
				return true
			}
			name := path.Base(words[0])
			switch {
			case fileWritingCommands[name]:
				reason = name
			case name == "git" && len(words) > 1 && mutatingGitCommands[words[1]]:
				reason = "git " + words[1]
			case name == "sed" || name == "perl" || name == "ruby":
				for _, w := range words[1:] {
					if shortFlagsContain(w, 'i') || strings.HasPrefix(w, "--in-place") {
						reason = name + " in-place edit"
						break
					}
				}
			}
		}
		return true
	})
	return reason
}

// checkTools are commands that are themselves a test, build or lint run.
var checkTools = map[string]bool{
	"ctest": true, "eslint": true, "golangci-lint": true, "jest": true, "make": true, "gmake": true,
	"mocha": true, "mypy": true, "nox": true, "phpunit": true, "py.test": true, "pytest": true,
	"rspec": true, "ruff": true, "shellcheck": true, "staticcheck": true, "tox": true, "tsc": true,
	"vitest": true, "mvn": true, "gradle": true, "gradlew": true, "mvnw": true,
}

// checkSubcommands are toolchains whose given subcommands run a check.
var checkSubcommands = map[string]map[string]bool{
	"go":     {"test": true, "vet": true, "build": true},
	"cargo":  {"test": true, "check": true, "build": true, "clippy": true, "nextest": true},
	"dotnet": {"test": true, "build": true},
	"swift":  {"test": true, "build": true},
	"bazel":  {"test": true, "build": true},
	"deno":   {"test": true, "check": true, "lint": true},
	"mix":    {"test": true},
	"rake":   {"test": true, "spec": true},
	"npx":    {"jest": true, "vitest": true, "tsc": true, "eslint": true, "mocha": true, "playwright": true},
	"bunx":   {"jest": true, "vitest": true, "tsc": true, "eslint": true},
}

// packageManagers run a check through "test" or a script named for one.
var packageManagers = map[string]bool{"npm": true, "pnpm": true, "yarn": true, "bun": true}

func callRunsCheck(words []string) bool {
	if len(words) == 0 || words[0] == "" {
		return false
	}
	name := path.Base(words[0])
	arg := func(i int) string {
		if i < len(words) {
			return words[i]
		}
		return ""
	}
	switch {
	case checkTools[name]:
		return true
	case checkSubcommands[name] != nil:
		return checkSubcommands[name][arg(1)]
	case packageManagers[name]:
		script := arg(1)
		if script == "run" {
			script = arg(2)
		}
		for _, hint := range []string{"test", "lint", "check", "build", "type"} {
			if strings.Contains(script, hint) {
				return true
			}
		}
		return script == "t"
	case name == "python" || name == "python3":
		if arg(1) == "-m" {
			switch arg(2) {
			case "pytest", "unittest", "mypy", "ruff", "tox":
				return true
			}
		}
	case name == "bundle" && arg(1) == "exec":
		return callRunsCheck(words[2:])
	}
	return false
}

// CommandRunsCheck reports whether any simple command in command is a
// recognised test, build or lint run (go test, make, npm test, pytest, ...).
func CommandRunsCheck(command string) bool {
	file := parseCommand(command)
	if file == nil {
		return false
	}
	found := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok && !found {
			found = callRunsCheck(literalWords(call))
		}
		return !found
	})
	return found
}
