package settings

import (
	"path"
	"strings"
)

// BashAutoModeWrites analyses a bash command line for auto mode's
// protected-path check (internal/claude/permission/protected.go): the files
// it writes, resolved as the deny-rule checks resolve them; whether kiln can
// name every one (complete: parsed, nothing run it cannot name, no operand
// it cannot resolve); and whether it changes git's own configuration — git
// config, a remote, or a -c option — which runs code later without naming a
// file.
func BashAutoModeWrites(cmd, cwd string) (writes []string, complete, gitConfig bool) {
	c := newMatchCtx(cwd)
	a := analyzeBash(cmd, c.cwd, c.home, true)
	complete = a.parsed && !a.unknown && !a.unsure
	for _, bc := range a.cmds {
		if gitChangesConfig(bc.name, bc.line) {
			gitConfig = true
		}
	}
	return a.writes, complete, gitConfig
}

// gitChangesConfig reports a git invocation that writes git's
// configuration: git config (other than a read), a remote change, or a -c
// option, which sets configuration (core.fsmonitor, core.sshCommand,
// alias.*) for the command it runs.
func gitChangesConfig(name, line string) bool {
	if path.Base(name) != "git" {
		return false
	}
	words := strings.Fields(line)
	i := 0
	if i < len(words) && path.Base(words[i]) == "git" {
		i++
	}
	// Global options before the subcommand.
	for i < len(words) && strings.HasPrefix(words[i], "-") {
		w := words[i]
		switch {
		case w == "-c" || strings.HasPrefix(w, "-c") && len(w) > 2 || w == "--config-env" || strings.HasPrefix(w, "--config-env="):
			return true
		case w == "-C" || w == "--git-dir" || w == "--work-tree" || w == "--namespace" || w == "--exec-path":
			i++ // the option's value
		}
		i++
	}
	if i >= len(words) {
		return false
	}
	sub, args := words[i], words[i+1:]
	switch sub {
	case "config":
		for _, a := range args {
			switch a {
			case "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l", "get", "list":
				return false
			}
		}
		return true
	case "remote":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "add", "set-url", "rename", "remove", "rm", "set-head", "set-branches":
			return true
		}
	}
	return false
}
