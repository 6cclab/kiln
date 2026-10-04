package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git directories inside the writable roots hold what git executes or
// follows later, outside the sandbox: hooks, config (core.fsmonitor,
// filters), commondir and gitdir (which point git at another directory's
// hooks and config). The sandbox lets a command write only what git itself
// writes there.

// gitWritableDirs and gitWritableFiles are what git writes inside a git
// directory (and inside the git directories nested in it under modules/
// and worktrees/): object and ref storage, logs, the index, the HEAD files
// and messages of commits, merges, rebases and fetches, and their lock
// files.
//
// Seatbelt's regular expressions are a small dialect (a character class
// holding "." or "-" fails to parse, a starred group is refused), so each
// entry is its own plain expression.
var (
	gitWritableDirs  = []string{"objects", "refs", "logs", "rebase-merge", "rebase-apply", "sequencer", "rr-cache", "lfs"}
	gitWritableFiles = []string{`index`, `index\.stash\.[0-9]+`, `next-index-[0-9]+`, `sharedindex\.[0-9a-f]+`,
		"HEAD", "ORIG_HEAD", "FETCH_HEAD", "MERGE_HEAD", "MERGE_MSG", "MERGE_MODE", "MERGE_RR",
		"CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "AUTO_MERGE", "COMMIT_EDITMSG", "SQUASH_MSG",
		"TAG_EDITMSG", "BISECT_LOG", "BISECT_START", "BISECT_TERMS", "BISECT_EXPECTED_REV",
		"BISECT_ANCESTORS_OK", "BISECT_NAMES", "BISECT_RUN", "BISECT_HEAD",
		"packed-refs", "shallow", `gc\.log`, `gc\.pid`}
)

// gitInitFiles are what git init writes at the top of a new git directory
// besides gitWritable: the config, the description, info/exclude and the
// sample hooks (inert: git runs a hook only under its own name). A
// command may write them only in a git directory that did not exist when
// it started (fresh), and kiln cleans that directory after the command
// (sanitizeNewGitDir).
//
// t[^/]+ is the temporary file git init makes there (mkstemp "tXXXXXX")
// to probe file modes and symlinks, and removes.
var gitInitFiles = []string{`config`, `config\.lock`, `description`, `info/exclude`, `info/exclude\.lock`, `hooks/[^/]+\.sample`, `t[^/]+`}

// gitDirRules renders a git directory for Seatbelt as deny-by-default with
// the writable entries re-opened. New directories may be made under
// modules/ and worktrees/ (where git creates a submodule's or a worktree's
// git directory), but only the writable files can go in them, so cloning
// a submodule or adding a worktree, which write a config or gitdir file,
// does not work inside the sandbox. fresh is for a git directory that does
// not exist yet: the command may create it and what git init writes in it
// (gitInitFiles), never a hook under a name git runs, commondir, gitdir,
// config.worktree or info/attributes.
func gitDirRules(gitdir string, fresh bool) []string {
	g := realPath(gitdir)
	if checkPath(g) != nil {
		return []string{`(deny file-write* (regex #"^/"))`}
	}
	q := regexQuote(g)
	nested := `(/(modules|worktrees)/.+)?`
	var allow []string
	for _, d := range gitWritableDirs {
		allow = append(allow, `(regex #"^`+q+nested+`/`+d+`(/.*)?$")`)
	}
	for _, f := range gitWritableFiles {
		allow = append(allow, `(regex #"^`+q+nested+`/`+f+`$")`, `(regex #"^`+q+nested+`/`+f+`\.lock$")`)
	}
	rules := []string{
		`(deny file-write* (subpath "` + g + `"))`,
		`(allow file-write* ` + strings.Join(allow, " ") + `)`,
		`(allow file-write-create (require-all (vnode-type DIRECTORY) (regex #"^` + q + `/(modules|worktrees)/")))`,
	}
	if fresh {
		var init []string
		for _, f := range gitInitFiles {
			init = append(init, `(regex #"^`+q+`/`+f+`$")`)
		}
		// The directory itself and hooks/, info/ may only be made as
		// directories: a .git file ("gitdir: elsewhere") or a link would
		// point git at a directory the command filled.
		rules = append(rules,
			`(allow file-write* `+strings.Join(init, " ")+`)`,
			`(allow file-write-create (require-all (vnode-type DIRECTORY) (regex #"^`+q+`(/hooks|/info|/branches)?$")))`)
	}
	return rules
}

// gitSnapshot records which gitSensitive entries exist in the git
// directories of a plan, before a Linux command runs.
type gitSnapshot map[string]bool

func snapshotGitDirs(dirs []string) gitSnapshot {
	s := gitSnapshot{}
	for _, d := range dirs {
		// Every directory under modules/ and worktrees/, set up or not: a
		// command can make one and write a config there that a later git
		// (or a HEAD written after the command) would take up.
		for _, g := range walkNested(d, false) {
			for _, e := range gitSensitive {
				p := filepath.Join(g, e)
				if _, err := os.Lstat(p); err == nil {
					s[p] = true
				}
			}
		}
	}
	return s
}

// sweepGitDirs removes the gitSensitive entries that appeared in the git
// directories while a Linux command ran (the existing ones were bound
// read-only; a new one cannot be, as bubblewrap binds only what exists).
// Claude Code's Linux sandbox does the same for a top-level HEAD, objects
// or refs that appears during a command.
func sweepGitDirs(dirs []string, before gitSnapshot) []string {
	var removed []string
	for p := range snapshotGitDirs(dirs) {
		if !before[p] {
			if os.RemoveAll(p) == nil {
				removed = append(removed, p)
			}
		}
	}
	return removed
}

// sanitizeNewGitDir cleans a git directory a sandboxed command created
// (git init), so nothing in it runs code when git is later used outside
// the sandbox: hooks under names git runs (only *.sample files stay), the
// redirect files (commondir, gitdir, config.worktree, info/attributes),
// the sensitive entries of any nested module or worktree directory, and
// every config key but those git init, a remote and a branch need
// (keptGitConfigKey). An entry that should be a file or directory but is
// a link is removed. On macOS the command's own profile already refused
// most of these (gitDirRules, fresh); on Linux this is the protection. It
// returns what it removed or rewrote.
func sanitizeNewGitDir(gitdir string) []string {
	fi, err := os.Lstat(gitdir)
	if err != nil {
		return nil
	}
	if !fi.IsDir() {
		// A .git file ("gitdir: <path>") or link points git at a
		// directory the command could fill: not a repository kiln keeps.
		if os.RemoveAll(gitdir) == nil {
			return []string{gitdir}
		}
		return nil
	}
	var changed []string
	remove := func(p string) {
		if _, err := os.Lstat(p); err == nil && os.RemoveAll(p) == nil {
			changed = append(changed, p)
		}
	}
	hooks := filepath.Join(gitdir, "hooks")
	if hi, err := os.Lstat(hooks); err == nil {
		if !hi.IsDir() {
			remove(hooks)
		} else if entries, err := os.ReadDir(hooks); err == nil {
			for _, e := range entries {
				if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".sample") {
					remove(filepath.Join(hooks, e.Name()))
				}
			}
		}
	}
	if ii, err := os.Lstat(filepath.Join(gitdir, "info")); err == nil && !ii.IsDir() {
		remove(filepath.Join(gitdir, "info"))
	}
	for _, e := range []string{"commondir", "gitdir", "config.worktree", filepath.Join("info", "attributes")} {
		remove(filepath.Join(gitdir, e))
	}
	for _, g := range walkNested(gitdir, false)[1:] {
		for _, e := range gitSensitive {
			remove(filepath.Join(g, e))
		}
	}
	cfg := filepath.Join(gitdir, "config")
	if ci, err := os.Lstat(cfg); err == nil {
		if !ci.Mode().IsRegular() {
			remove(cfg)
		} else if rewrote, err := filterGitConfig(cfg); err != nil {
			remove(cfg) // unreadable, or no git to read it with: drop it
		} else if rewrote {
			changed = append(changed, cfg)
		}
	}
	return changed
}

// keptGitConfigKey reports a config key (as git config --list prints it:
// section and name lower-cased) a new repository keeps: what git init
// writes, the user's identity, a remote's URL and refspec, and a branch's
// upstream. Everything else (core.hooksPath, core.fsmonitor,
// core.sshCommand, filters, aliases, include.path, ...) is dropped.
func keptGitConfigKey(key string) bool {
	switch key {
	case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates",
		"core.ignorecase", "core.precomposeunicode", "core.symlinks",
		"extensions.objectformat", "extensions.refstorage", "user.name", "user.email":
		return true
	}
	section, rest, ok := strings.Cut(key, ".")
	if !ok {
		return false
	}
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 {
		return false
	}
	switch name := rest[i+1:]; section {
	case "remote":
		return name == "url" || name == "fetch"
	case "branch":
		return name == "remote" || name == "merge"
	}
	return false
}

// filterGitConfig rewrites cfg with only the kept keys, when it holds any
// other. git itself reads the file (includes not followed), so git's own
// parsing decides what the keys are.
func filterGitConfig(cfg string) (rewrote bool, err error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return false, err
	}
	out, err := exec.Command(git, "config", "--file", cfg, "--no-includes", "--null", "--list").Output()
	if err != nil {
		return false, err
	}
	type kv struct{ k, v string }
	var keep []kv
	dropped := false
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		k, v, _ := strings.Cut(entry, "\n")
		if keptGitConfigKey(k) {
			keep = append(keep, kv{k, v})
		} else {
			dropped = true
		}
	}
	if !dropped {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(cfg), ".config-kiln-*")
	if err != nil {
		return false, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	for _, e := range keep {
		if err := exec.Command(git, "config", "--file", tmp.Name(), "--add", e.k, e.v).Run(); err != nil {
			return false, err
		}
	}
	return true, os.Rename(tmp.Name(), cfg)
}
