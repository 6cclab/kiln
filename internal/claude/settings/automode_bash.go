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
		if unmodelledWriter(bc.name, bc.line) {
			complete = false
		}
	}
	return a.writes, complete, gitConfig
}

// unmodelledWriters are programs that write files kiln's analysis does not
// name: a download's output (curl -o, wget, wget -O), an archive's members
// (tar -x -C, unzip -d), a sync or copy over the network, a patch, the
// pieces of a split. Auto mode cannot tell what they write, so it
// classifies them even past a narrow allow rule.
var unmodelledWriters = set(
	"curl", "wget", "aria2c", "tar", "bsdtar", "gtar", "unzip", "zip", "7z", "7za", "7zz", "unrar",
	"gzip", "gunzip", "bzip2", "bunzip2", "xz", "unxz", "zstd", "unzstd", "lz4", "cpio", "pax", "ditto",
	"rsync", "scp", "sftp", "patch", "split", "csplit", "hdiutil",
)

// gitFileWriters are git subcommands that write files outside the
// repository's own bookkeeping, at a path they are given.
var gitFileWriters = set("clone", "init", "worktree", "archive", "bundle", "format-patch", "submodule")

// unmodelledWriter reports a command whose written files kiln cannot name.
func unmodelledWriter(name, line string) bool {
	name = path.Base(name)
	if unmodelledWriters[name] {
		return true
	}
	if name != "git" {
		return false
	}
	words := strings.Fields(line)
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "-C" || w == "-c" || w == "--git-dir" || w == "--work-tree" || w == "--namespace" || w == "--exec-path":
			i++
		case strings.HasPrefix(w, "-"):
		default:
			return gitFileWriters[w]
		}
	}
	return false
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
