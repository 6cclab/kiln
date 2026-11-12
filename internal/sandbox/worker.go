package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/andrepato/harness/internal/execenv"
)

type workerScope struct {
	parent  string
	child   string
	temp    string
	gitDirs []string
}

// NewWorker creates a mandatory sandbox for a worker in a linked worktree.
// parentRoot is the logical checkout whose settings apply; worktreeRoot is
// the worker's checkout. The caller must use the returned manager for every
// worker shell command, must reject an unsandboxed command request, and must
// Close it when the worker exits. An unavailable mechanism is an error.
// Worker profiles currently require macOS Seatbelt; Linux's bubblewrap
// profile cannot deny arbitrary visible Unix sockets.
func NewWorker(parent Config, opts Options, parentRoot, worktreeRoot string) (*Manager, error) {
	if runtime.GOOS != "darwin" || (opts.GOOS != "" && opts.GOOS != "darwin") {
		return nil, fmt.Errorf("sandbox: worker isolation requires macOS Seatbelt")
	}
	if parent.FilesystemDisabled {
		return nil, fmt.Errorf("sandbox: worker isolation requires filesystem restrictions")
	}
	pr, err := existingDir(parentRoot)
	if err != nil {
		return nil, fmt.Errorf("sandbox: parent checkout: %w", err)
	}
	ch, err := existingDir(worktreeRoot)
	if err != nil {
		return nil, fmt.Errorf("sandbox: worker worktree: %w", err)
	}
	if overlaps(pr, ch) {
		return nil, fmt.Errorf("sandbox: parent and worker checkouts overlap")
	}
	if fi, err := os.Lstat(filepath.Join(ch, ".git")); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("sandbox: worker checkout must be a linked Git worktree")
	}
	gitDirs := worktreeGitDirs(ch)
	if len(gitDirs) == 0 {
		return nil, fmt.Errorf("sandbox: cannot resolve worker Git metadata")
	}
	for i, g := range gitDirs {
		gitDirs[i] = realPath(g)
	}
	if err := rejectWorkerHardlinks(ch); err != nil {
		return nil, err
	}
	base, err := execenv.EnsureTempRoot()
	if err != nil {
		return nil, fmt.Errorf("sandbox: worker temp root: %w", err)
	}
	if overlaps(base, pr) || overlaps(base, ch) {
		return nil, fmt.Errorf("sandbox: worker temp root overlaps a checkout")
	}
	tmp, err := os.MkdirTemp(base, "worker-")
	if err != nil {
		return nil, fmt.Errorf("sandbox: worker private temp: %w", err)
	}
	cfg := workerConfig(parent, pr, ch)
	opts.Cwd = ch
	opts.Roots = func() []string { return []string{ch} }
	opts.TmpDir = tmp
	m := New(cfg, opts)
	m.worker = &workerScope{parent: pr, child: ch, temp: tmp, gitDirs: gitDirs}
	if err := m.Unavailable(); err != nil {
		m.Close()
		return nil, fmt.Errorf("sandbox: worker isolation unavailable: %w", err)
	}
	return m, nil
}

func existingDir(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q is not absolute", p)
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(r)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%q is not a directory", p)
	}
	return r, nil
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func workerConfig(parent Config, parentRoot, child string) Config {
	c := parent
	c.Enabled = true
	c.FailIfUnavailable = true
	c.AutoAllow = false
	c.AllowUnsandboxed = false
	c.Excluded = nil
	c.FilesystemDisabled = false
	c.AllowWrite = nil
	c.HTTPProxyPort = 0
	c.SOCKSProxyPort = 0
	c.AllowedDomains = nil
	c.StrictAllowlist = true
	c.AllowLocalBinding = false
	c.AllowAllUnixSockets = false
	c.UnixSockets = nil
	c.MachLookup = nil
	c.WeakerNested = false
	c.WeakerNetwork = false
	c.AppleEvents = false
	c.DenyRead = inheritedRules(parent.DenyRead, parentRoot, child)
	c.DenyWrite = inheritedRules(parent.DenyWrite, parentRoot, child)
	c.AllowRead = inheritedRules(parent.AllowRead, parentRoot, child)
	return c
}

func inheritedRules(rules []Rule, parentRoot, child string) []Rule {
	out := make([]Rule, 0, len(rules)*2)
	for _, r := range rules {
		out = append(out, Rule{Path: r.Path, Segs: append([]string(nil), r.Segs...)})
		seen := map[string]bool{}
		for _, base := range []string{filepath.Clean(r.Path), realPath(r.Path)} {
			rel, err := filepath.Rel(parentRoot, base)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			mapped := filepath.Join(child, rel)
			if !seen[mapped] {
				out = append(out, Rule{Path: mapped, Segs: append([]string(nil), r.Segs...)})
				seen[mapped] = true
			}
		}
	}
	return out
}
