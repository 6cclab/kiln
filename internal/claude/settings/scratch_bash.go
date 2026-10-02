package settings

// scratchFileCmds are file commands that only create, copy, move or remove
// the files they name: allowed in a command that writes nothing but the
// session scratchpad.
var scratchFileCmds = map[string]bool{
	"mkdir": true, "touch": true, "cp": true, "mv": true, "tee": true, "rm": true, "rmdir": true, "ln": true,
}

// BashWritesOnlyInside reports a bash command line that writes files only
// where inside allows (the session scratchpad) and reads only where
// readable or inside allows: kiln must be able to analyse all of it, every
// command in it must be read-only or a plain file command (mkdir, touch,
// cp, mv, tee, rm, rmdir, ln), and it must write at least one file.
func BashWritesOnlyInside(cmd, cwd string, inside, readable func(string) bool) bool {
	c := newMatchCtx(cwd)
	a := analyzeBash(cmd, c.cwd, c.home, true)
	if !a.parsed || a.unknown || a.unsure || len(a.writes) == 0 || len(a.cmds) == 0 {
		return false
	}
	for _, bc := range a.cmds {
		if !bc.readOnly && !scratchFileCmds[bc.name] {
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
