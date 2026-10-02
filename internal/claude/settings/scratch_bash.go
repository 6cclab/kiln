package settings

import "strings"

// scratchFileCmds are file commands that only create, copy, move or remove
// the files they name: allowed in a command that writes nothing but the
// session scratchpad. ln is not one: a link made earlier in a command line
// turns a later write in the same line ("ln -s ~ $S/l && echo x > $S/l/f")
// into a write elsewhere, and the analysis sees the filesystem as it is
// before the line runs. For the same reason cp and mv take only the
// options in scratchFlags, none of which copies a link or names a
// destination (-R, -a, -P, -l, -s, -t, --target-directory).
var scratchFileCmds = map[string]bool{
	"mkdir": true, "touch": true, "cp": true, "mv": true, "tee": true, "rm": true, "rmdir": true,
}

// scratchFlags are the single-letter options cp and mv may carry in a
// scratchpad command; any other option, long options included, takes the
// command off the fast path.
var scratchFlags = map[string]string{
	"cp": "finpv",
	"mv": "finv",
}

// scratchFlagsOK reports a command whose options are all ones the
// scratchpad allows for it.
func scratchFlagsOK(name, line string) bool {
	allowed, limited := scratchFlags[name]
	if !limited {
		return true
	}
	words := strings.Fields(line)
	for _, w := range words[min(1, len(words)):] {
		if w == "--" {
			return true
		}
		if !strings.HasPrefix(w, "-") || w == "-" {
			continue
		}
		if strings.HasPrefix(w, "--") {
			return false
		}
		for _, c := range w[1:] {
			if !strings.ContainsRune(allowed, c) {
				return false
			}
		}
	}
	return true
}

// BashWritesOnlyInside reports a bash command line that writes files only
// where inside allows (the session scratchpad) and reads only where
// readable or inside allows: kiln must be able to analyse all of it, every
// command in it must be read-only or a plain file command (mkdir, touch,
// cp, mv, tee, rm, rmdir) with only the options scratchFlags allows, and
// it must write at least one file.
func BashWritesOnlyInside(cmd, cwd string, inside, readable func(string) bool) bool {
	c := newMatchCtx(cwd)
	a := analyzeBash(cmd, c.cwd, c.home, true)
	if !a.parsed || a.unknown || a.unsure || len(a.writes) == 0 || len(a.cmds) == 0 {
		return false
	}
	for _, bc := range a.cmds {
		if !bc.readOnly && !(scratchFileCmds[bc.name] && scratchFlagsOK(bc.name, bc.line)) {
			return false
		}
	}
	for _, w := range a.writes {
		if !inside(w) {
			return false
		}
	}
	for _, r := range a.reads {
		if !inside(r) && !readable(r) {
			return false
		}
	}
	return true
}
