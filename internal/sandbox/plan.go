package sandbox

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Plan is everything one sandboxed command's profile is built from: the
// session Config resolved against the current workspace roots, the
// temp directory and the proxy ports. Paths are absolute.
type Plan struct {
	Cwd string
	// WriteRoots may be written, with everything under them: the
	// workspace roots (cwd and /add-dir), the sandbox temp directory,
	// allowWrite entries without a wildcard, and a linked worktree's
	// shared git directory.
	WriteRoots []string
	// WriteGlobs are allowWrite entries with a wildcard (macOS only; the
	// Linux sandbox mounts concrete paths).
	WriteGlobs []Rule
	// DenyWrite holds even inside WriteRoots: denyWrite entries and the
	// protected paths.
	DenyWrite []Rule
	// DenyWriteLiteral are paths whose own directory entry may not be
	// created, renamed or removed (the parents of protected paths), so a
	// protected path cannot be swapped for one written elsewhere.
	DenyWriteLiteral []string
	DenyRead         []Rule
	AllowRead        []Rule
	TmpDir           string
	// GitDirs are the git directories inside the writable roots (each
	// root's .git, a linked worktree's): only what git itself writes may
	// be written there (seatbelt.go gitDirRules; Linux: sweepGitDirs).
	GitDirs []string
	// NewGitDirs are workspace roots' .git directories that do not exist
	// yet: a command may create one (git init), writing only what git
	// writes plus what git init adds (seatbelt.go gitDirRules), and kiln
	// cleans it after the command (gitdir.go sanitizeNewGitDir).
	NewGitDirs []string
	// Placeholders are empty read-only files kiln created (Linux) where a
	// protected path did not exist yet, so it cannot be created.
	Placeholders []string

	FilesystemDisabled bool

	// HTTPProxyPort and SOCKSProxyPort are the loopback ports sandboxed
	// commands may connect to; 0 for none.
	HTTPProxyPort  int
	SOCKSProxyPort int

	AllowLocalBinding   bool
	AllowAllUnixSockets bool
	UnixSockets         []string
	// HiddenSockets are well-known Unix sockets (the session bus, docker,
	// the ssh agent) a Linux sandboxed command must not reach: bwrap binds
	// /dev/null over each one that exists. Seatbelt needs no list; it
	// denies every Unix socket the settings do not name.
	HiddenSockets []string
	MachLookup    []string
	WeakerNested  bool
	WeakerNetwork bool
	AppleEvents   bool

	Env   map[string]string
	Unset []string
}

// buildPlan resolves cfg for one command run in cwd with the given
// workspace roots.
func buildPlan(cfg Config, cwd string, roots []string, tmpDir, home string, httpPort, socksPort int) Plan {
	p := Plan{
		Cwd:                 cwd,
		TmpDir:              tmpDir,
		FilesystemDisabled:  cfg.FilesystemDisabled,
		HTTPProxyPort:       httpPort,
		SOCKSProxyPort:      socksPort,
		AllowLocalBinding:   cfg.AllowLocalBinding,
		AllowAllUnixSockets: cfg.AllowAllUnixSockets,
		UnixSockets:         cfg.UnixSockets,
		MachLookup:          cfg.MachLookup,
		WeakerNested:        cfg.WeakerNested,
		WeakerNetwork:       cfg.WeakerNetwork,
		AppleEvents:         cfg.AppleEvents,
		HiddenSockets:       hiddenSockets(cfg, os.Getuid(), os.Getenv),
		DenyRead:            cfg.DenyRead,
		AllowRead:           cfg.AllowRead,
	}
	writable := append([]string(nil), roots...)
	if tmpDir != "" {
		writable = append(writable, tmpDir)
	}
	for _, r := range cfg.AllowWrite {
		if r.glob() {
			p.WriteGlobs = append(p.WriteGlobs, r)
		} else {
			writable = append(writable, r.Path)
		}
	}
	gitDirs := worktreeGitDirs(cwd)
	writable = append(writable, gitDirs...)
	p.WriteRoots = dedupe(writable)
	var gds []string
	for _, r := range roots {
		g := filepath.Join(r, ".git")
		switch {
		case isDir(g):
			gds = append(gds, g)
		case !exists(g):
			p.NewGitDirs = append(p.NewGitDirs, g)
		}
	}
	p.GitDirs = dedupe(append(gds, gitDirs...))
	p.NewGitDirs = dedupe(p.NewGitDirs)

	p.DenyWrite = append([]Rule(nil), cfg.DenyWrite...)
	prot, literal := protectedPaths(roots, home, gitDirs)
	p.DenyWrite = append(p.DenyWrite, prot...)
	p.DenyWriteLiteral = literal

	p.Env = map[string]string{"KILN_SANDBOX": "1"}
	if tmpDir != "" && !cfg.FilesystemDisabled {
		p.Env["TMPDIR"] = tmpDir
	}
	if httpPort > 0 {
		u := "http://127.0.0.1:" + itoa(httpPort)
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			p.Env[k] = u
		}
		all := u
		if socksPort > 0 {
			all = "socks5h://127.0.0.1:" + itoa(socksPort)
		}
		p.Env["ALL_PROXY"], p.Env["all_proxy"] = all, all
		// No NO_PROXY: a sandboxed command has no direct route to
		// loopback, so localhost goes through the proxy as well (where it
		// needs an exact allowedDomains entry), and an inherited NO_PROXY
		// would only send requests where they cannot go.
		p.Unset = append(p.Unset, "NO_PROXY", "no_proxy")
	}
	p.Unset = append(p.Unset, cfg.DenyEnv...)
	return p
}

// hiddenSockets lists the Unix sockets the Linux sandbox hides: in its
// own network namespace a command still reaches any socket file it can
// see, and these hand out control of the user's session (D-Bus, systemd),
// the host (docker, podman) or the user's keys (ssh and gpg agents).
// allowAllUnixSockets hides none; an allowUnixSockets entry un-hides the
// sockets at or under it.
func hiddenSockets(cfg Config, uid int, getenv func(string) string) []string {
	if cfg.AllowAllUnixSockets {
		return nil
	}
	run := "/run/user/" + strconv.Itoa(uid)
	list := []string{
		run + "/bus", run + "/systemd/private", run + "/docker.sock",
		run + "/podman/podman.sock", run + "/gnupg/S.gpg-agent", run + "/gnupg/S.gpg-agent.ssh",
		"/run/dbus/system_bus_socket", "/var/run/dbus/system_bus_socket",
		"/run/docker.sock", "/var/run/docker.sock", "/run/podman/podman.sock",
		"/run/containerd/containerd.sock",
	}
	if rt := getenv("XDG_RUNTIME_DIR"); rt != "" && rt != run {
		list = append(list, rt+"/bus", rt+"/docker.sock", rt+"/podman/podman.sock")
	}
	for _, k := range []string{"SSH_AUTH_SOCK", "DOCKER_HOST"} {
		v := strings.TrimPrefix(getenv(k), "unix://")
		if filepath.IsAbs(v) {
			list = append(list, filepath.Clean(v))
		}
	}
	var out []string
	for _, s := range dedupe(list) {
		allowed := false
		for _, a := range cfg.UnixSockets {
			if a = filepath.Clean(a); s == a || strings.HasPrefix(s, a+"/") {
				allowed = true
			}
		}
		if !allowed {
			out = append(out, s)
		}
	}
	return out
}

// shellStartupFiles are written to only by the user: a command that could
// edit them runs code in every later shell, outside the sandbox.
var shellStartupFiles = []string{
	".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile",
	".zshrc", ".zshenv", ".zprofile", ".zlogin", ".zlogout",
	".gitconfig",
}

// claudeConfigEntries are the .claude entries Claude Code loads
// configuration or code from (code.claude.com/docs/en/sandboxing,
// "Protected paths"); kiln's own .kiln directory is protected whole.
var claudeConfigEntries = []string{
	"settings.json", "settings.local.json", "skills", "agents", "commands",
	"hooks", "workflows", "scheduled_tasks.json",
}

// protectedPaths are the paths sandboxed commands may not write even
// inside a writable root, following Claude Code's protected paths:
//
//   - in each workspace root and every directory above it: the .claude
//     settings files and its skills, agents, commands, hooks and workflows,
//     .mcp.json, and kiln's .kiln directory;
//   - the same at any depth inside a workspace root (a nested project's
//     .claude, .mcp.json, .kiln and .git/hooks), as kiln trusts a folder's
//     subdirectories with it (macOS: by pattern; Linux binds paths that
//     exist, so it holds the ones in the roots and their parents);
//   - in each workspace root: shell startup files, .gitconfig, .vscode,
//     .idea, and in its git directory what git reads code or redirection
//     from (gitDirProtections);
//   - files that would turn a workspace root into a bare git repository:
//     HEAD, objects and refs at the top level, config unless it is a
//     directory with no HEAD beside it, and hooks when a HEAD exists;
//   - in the home directory, whatever the roots: ~/.claude, ~/.claude.json,
//     kiln's ~/.kiln and ~/.harness, shell startup files, and the places
//     that start programs at login (~/Library/LaunchAgents,
//     ~/.config/autostart, ~/.config/systemd), so a home directory added
//     as a workspace root does not open them;
//   - a linked worktree's git directories.
//
// literal lists the directories whose own entry is held (.claude, .git),
// so one cannot be renamed away and replaced.
//
// This is not the permission gate's protected-path list
// (permission/protected.go), and is narrower on purpose: that list names
// what needs the user's approval before a command runs (all of .git and
// .claude, package-manager and hook-runner config), while this one is
// enforced by the kernel on every sandboxed process and must leave git,
// builds and installs working. A sandboxed command that writes a path on
// the gate's list, as far as kiln can name its writes, is not auto-allowed
// by the sandbox: it asks, or in auto mode is classified.
func protectedPaths(roots []string, home string, gitDirs []string) (rules []Rule, literal []string) {
	add := func(p string) { rules = append(rules, Rule{Path: p}) }
	seen := map[string]bool{}
	for _, root := range roots {
		for d := root; ; d = filepath.Dir(d) {
			if !seen[d] {
				seen[d] = true
				for _, e := range claudeConfigEntries {
					add(filepath.Join(d, ".claude", e))
				}
				add(filepath.Join(d, ".mcp.json"))
				add(filepath.Join(d, ".kiln"))
				literal = append(literal, filepath.Join(d, ".claude"))
			}
			if d == filepath.Dir(d) {
				break
			}
		}
		for _, e := range claudeConfigEntries {
			rules = append(rules, Rule{Path: root, Segs: []string{"**", ".claude", e}})
		}
		for _, e := range []string{".mcp.json", ".kiln"} {
			rules = append(rules, Rule{Path: root, Segs: []string{"**", e}})
		}
		// Nested repositories' git directories (at least one level down;
		// the root's own .git is handled below, existing or not).
		for _, s := range gitSensitive {
			rules = append(rules, Rule{Path: root, Segs: []string{"*", "**", ".git", s}})
		}
		for _, f := range shellStartupFiles {
			add(filepath.Join(root, f))
		}
		for _, d := range []string{".vscode", ".idea"} {
			add(filepath.Join(root, d))
		}
		// An existing .git is held in place with its sensitive entries; a
		// missing one may be created (git init), under NewGitDirs' rules.
		if exists(filepath.Join(root, ".git")) {
			rules = append(rules, gitDirProtections(filepath.Join(root, ".git"))...)
			literal = append(literal, filepath.Join(root, ".git"))
		}

		// A bare repository at the root: git would read hooks and config
		// from the top level.
		_, headErr := os.Lstat(filepath.Join(root, "HEAD"))
		hasHead := headErr == nil
		for _, name := range []string{"HEAD", "objects", "refs"} {
			if _, err := os.Lstat(filepath.Join(root, name)); err != nil || hasHead {
				add(filepath.Join(root, name))
			}
		}
		if fi, err := os.Lstat(filepath.Join(root, "config")); err != nil || !fi.IsDir() || hasHead {
			add(filepath.Join(root, "config"))
		}
		if hasHead {
			add(filepath.Join(root, "hooks"))
		}
	}
	if home != "" {
		for _, p := range []string{".claude", ".claude.json", ".kiln", ".harness",
			filepath.Join("Library", "LaunchAgents"), filepath.Join(".config", "autostart"), filepath.Join(".config", "systemd")} {
			add(filepath.Join(home, p))
		}
		for _, f := range shellStartupFiles {
			add(filepath.Join(home, f))
		}
	}
	for _, g := range gitDirs {
		rules = append(rules, gitDirProtections(g)...)
	}
	return rules, dedupe(literal)
}

// gitSensitive are the entries of a git directory git reads code or
// redirection from: hooks; config and config.worktree (core.fsmonitor,
// filters, hooksPath); commondir and gitdir, which point git at another
// directory's hooks and config; info (attributes select filters).
var gitSensitive = []string{"hooks", "config", "config.worktree", "commondir", "gitdir", "info"}

// gitDirProtections lists, for a git directory and every git directory
// nested in it now (submodules under modules/, linked worktrees under
// worktrees/), the gitSensitive entries. On macOS the whole git directory
// is deny-by-default anyway (seatbelt.go, gitWritable); these paths are
// what Linux binds read-only, with sweepGitDirs removing any created
// during a command.
func gitDirProtections(gitdir string) []Rule {
	var out []Rule
	for _, g := range nestedGitDirs(gitdir) {
		for _, s := range gitSensitive {
			out = append(out, Rule{Path: filepath.Join(g, s)})
		}
	}
	return out
}

// nestedGitDirs returns gitdir and the git directories under its modules/
// and worktrees/ (directories holding a HEAD), not descending into objects,
// refs or logs.
func nestedGitDirs(gitdir string) []string { return walkNested(gitdir, true) }

// walkNested returns gitdir and the directories under its modules/ and
// worktrees/ (skipping object and ref storage); with needHead, only those
// holding a HEAD file, that is git directories already set up.
func walkNested(gitdir string, needHead bool) []string {
	out := []string{gitdir}
	for _, nested := range []string{"modules", "worktrees"} {
		_ = filepath.WalkDir(filepath.Join(gitdir, nested), func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			switch d.Name() {
			case "objects", "refs", "logs", "hooks", "info":
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "HEAD")); err == nil || !needHead {
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}

// worktreeGitDirs returns the git directories a linked worktree in cwd
// writes to (its own and the main repository's shared one), so commands
// such as git commit can update refs and the index. Empty when cwd is not
// a linked worktree.
func worktreeGitDirs(cwd string) []string {
	for d := cwd; ; d = filepath.Dir(d) {
		dotGit := filepath.Join(d, ".git")
		fi, err := os.Lstat(dotGit)
		if err == nil {
			if fi.IsDir() || !fi.Mode().IsRegular() {
				return nil // a main checkout: .git is already in the root
			}
			data, err := os.ReadFile(dotGit)
			if err != nil {
				return nil
			}
			line := strings.TrimSpace(string(data))
			gitdir, ok := strings.CutPrefix(line, "gitdir:")
			if !ok {
				return nil
			}
			gitdir = strings.TrimSpace(gitdir)
			if !filepath.IsAbs(gitdir) {
				gitdir = filepath.Join(d, gitdir)
			}
			gitdir = filepath.Clean(gitdir)
			dirs := []string{gitdir}
			if c, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
				common := strings.TrimSpace(string(c))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitdir, common)
				}
				dirs = append(dirs, filepath.Clean(common))
			}
			return dirs
		}
		if d == filepath.Dir(d) {
			return nil
		}
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// realPath resolves p's symlinks, the longest existing prefix of it when
// p itself does not exist yet, so a profile names what the kernel will
// see (/tmp is /private/tmp on macOS).
func realPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(realPath(parent), filepath.Base(p))
}

// spellings returns p as written and as resolved, deduplicated.
func spellings(p string) []string {
	r := realPath(p)
	if r == p {
		return []string{p}
	}
	return []string{p, r}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func itoa(n int) string { return strconv.Itoa(n) }
