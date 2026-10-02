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
	MachLookup          []string
	WeakerNested        bool
	WeakerNetwork       bool
	AppleEvents         bool

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
		noProxy := "localhost,127.0.0.1,::1"
		p.Env["NO_PROXY"], p.Env["no_proxy"] = noProxy, noProxy
	}
	p.Unset = append([]string(nil), cfg.DenyEnv...)
	return p
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
//   - in each workspace root: shell startup files, .gitconfig, .vscode,
//     .idea, and .git's hooks and config;
//   - files that would turn a workspace root into a bare git repository:
//     HEAD, objects and refs at the top level, config unless it is a
//     directory with no HEAD beside it, and hooks when a HEAD exists;
//   - ~/.claude, ~/.claude.json, and kiln's own ~/.kiln and ~/.harness;
//   - a linked worktree's shared git directory's hooks and config.
//
// literal lists the directories whose own entry is held (.claude, .kiln,
// .git, .vscode, .idea), so one cannot be renamed away and replaced.
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
		for _, f := range shellStartupFiles {
			add(filepath.Join(root, f))
		}
		for _, d := range []string{".vscode", ".idea"} {
			add(filepath.Join(root, d))
		}
		rules = append(rules, gitDirProtections(filepath.Join(root, ".git"))...)
		literal = append(literal, filepath.Join(root, ".git"))

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
		add(filepath.Join(home, ".claude"))
		add(filepath.Join(home, ".claude.json"))
		add(filepath.Join(home, ".kiln"))
		add(filepath.Join(home, ".harness"))
	}
	for _, g := range gitDirs {
		rules = append(rules, gitDirProtections(g)...)
	}
	return rules, dedupe(literal)
}

// gitDirProtections holds what git reads code from in a git directory:
// its hooks, config and config.worktree, and the same in every nested git
// directory under it — submodules' (modules/<name>) and linked worktrees'
// (worktrees/<name>). A submodule's config is reached by git commands kiln
// and the user run outside the sandbox (git status runs its
// core.fsmonitor), so it is as sensitive as the top-level one.
//
// The wildcard rules cover nested directories created later (macOS); the
// ones that exist now are also listed by path, for Linux, whose sandbox
// binds concrete paths only.
func gitDirProtections(gitdir string) []Rule {
	sensitive := []string{"hooks", "config", "config.worktree"}
	var out []Rule
	for _, s := range sensitive {
		out = append(out, Rule{Path: filepath.Join(gitdir, s)})
		for _, nested := range []string{"modules", "worktrees"} {
			out = append(out, Rule{Path: filepath.Join(gitdir, nested), Segs: []string{"**", s}})
		}
	}
	for _, nested := range []string{"modules", "worktrees"} {
		_ = filepath.WalkDir(filepath.Join(gitdir, nested), func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if _, err := os.Lstat(filepath.Join(p, "HEAD")); err == nil {
				for _, s := range sensitive {
					out = append(out, Rule{Path: filepath.Join(p, s)})
				}
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
