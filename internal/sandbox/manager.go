package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/andrepato/harness/internal/execenv"
)

// Options configures a Manager.
type Options struct {
	// Cwd is the primary working directory.
	Cwd string
	// Roots returns the current workspace roots (the permission gate's,
	// which /add-dir widens), read for every command. Nil means {Cwd}.
	Roots func() []string
	// Home is the home directory; "" reads it.
	Home string
	// TmpDir is the per-user directory sandboxed commands write
	// temporary files to ($TMPDIR inside the sandbox); "" picks one.
	TmpDir string
	// GOOS overrides runtime.GOOS (tests).
	GOOS string
	// LookPath overrides exec.LookPath (tests).
	LookPath func(string) (string, error)
	// Probe, when set, replaces the start-up check that the mechanism
	// actually runs (tests).
	Probe func(argv []string) error
	// LookupIP, when set, resolves hostnames for the proxy (tests).
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Manager is a session's sandbox: it decides which bash commands run
// sandboxed, wraps them for the platform, and runs the network proxy.
// It implements execenv.Sandbox.
type Manager struct {
	cfg  Config
	opts Options

	once     sync.Once
	mech     string // "sandbox-exec", "bwrap", ""
	availErr error
	bwrap    string
	socat    string

	mu       sync.Mutex
	proxy    *Proxy
	proxyErr error
	bridges  map[int]*bridge
	decide   func(ctx context.Context, host string, port int) (allow, always bool, err error)
	onSave   func(host string)
	closed   bool
	held     map[string]int // Linux placeholders in use (placeholders.go)
}

// New builds a Manager for cfg.
func New(cfg Config, opts Options) *Manager {
	if opts.Home == "" {
		opts.Home, _ = os.UserHomeDir()
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Roots == nil {
		cwd := opts.Cwd
		opts.Roots = func() []string { return []string{cwd} }
	}
	return &Manager{cfg: cfg, opts: opts, bridges: map[int]*bridge{}, held: map[string]int{}}
}

// Config returns the configuration the manager enforces.
func (m *Manager) Config() Config { return m.cfg }

// Enabled reports sandbox.enabled.
func (m *Manager) Enabled() bool { return m != nil && m.cfg.Enabled }

// Unavailable returns why the sandbox cannot run on this machine, or nil.
func (m *Manager) Unavailable() error {
	if m == nil {
		return errors.New("sandbox not configured")
	}
	m.once.Do(m.detect)
	return m.availErr
}

// Mechanism names what enforces the sandbox here ("sandbox-exec
// (Seatbelt)", "bubblewrap"), or "" where none exists.
func (m *Manager) Mechanism() string {
	if m == nil {
		return ""
	}
	m.once.Do(m.detect)
	return m.mech
}

// Active reports that sandboxing is enabled and can run: commands are
// sandboxed.
func (m *Manager) Active() bool { return m.Enabled() && m.Unavailable() == nil }

// AutoAllow is sandbox.autoAllowBashIfSandboxed.
func (m *Manager) AutoAllow() bool { return m != nil && m.cfg.AutoAllow }

// UnsandboxedAllowed is sandbox.allowUnsandboxedCommands.
func (m *Manager) UnsandboxedAllowed() bool { return m != nil && m.cfg.AllowUnsandboxed }

// OffersUnsandboxed reports whether the bash tools offer the
// dangerouslyDisableSandbox parameter: the sandbox runs and the retry is
// allowed (Claude Code ignores the parameter otherwise).
func (m *Manager) OffersUnsandboxed() bool { return m.Active() && m.cfg.AllowUnsandboxed }

// WillSandbox reports whether a bash call runs inside the sandbox: the
// sandbox is active, the call did not ask to run unsandboxed (or that is
// not allowed), and excludedCommands does not cover it.
func (m *Manager) WillSandbox(command string, disable bool) bool {
	if !m.Active() {
		return false
	}
	if disable && m.cfg.AllowUnsandboxed {
		return false
	}
	return !excluded(command, m.cfg.Excluded)
}

// CriticalRemoval reports an rm/rmdir of a critical path (critical.go).
func (m *Manager) CriticalRemoval(command string) bool {
	return criticalRemoval(command, m.opts.Cwd, m.opts.Home, m.opts.Roots())
}

// SetNetworkDecider binds who decides a host no list settles (the
// permission mode and the user), and onSave, called for a host the user
// chose never to be asked about again.
func (m *Manager) SetNetworkDecider(decide func(ctx context.Context, host string, port int) (allow, always bool, err error), onSave func(host string)) {
	// The proxy always asks decideNetwork, which reads these under mu.
	m.mu.Lock()
	m.decide, m.onSave = decide, onSave
	m.mu.Unlock()
}

func (m *Manager) decideNetwork(ctx context.Context, host string, port int) (bool, bool, error) {
	m.mu.Lock()
	decide, onSave := m.decide, m.onSave
	m.mu.Unlock()
	if decide == nil {
		return false, false, nil
	}
	allow, always, err := decide(ctx, host, port)
	if allow && always && onSave != nil {
		onSave(host)
	}
	return allow, always, err
}

// ForCommand implements execenv.Sandbox.
func (m *Manager) ForCommand(command string, disable bool) execenv.CommandSandbox {
	if m == nil || !m.WillSandbox(command, disable) {
		return nil
	}
	return &commandSandbox{m: m}
}

// Always returns a wrapper that runs a command inside the sandbox whatever
// excludedCommands says, or nil when the sandbox is not active. kiln uses
// it for its own git calls on a repository sandboxed commands may have
// written to.
func (m *Manager) Always() execenv.CommandSandbox {
	if m == nil || !m.Active() {
		return nil
	}
	return &commandSandbox{m: m}
}

// Close stops the proxy and relays.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.proxy != nil {
		_ = m.proxy.Close()
	}
	for _, b := range m.bridges {
		_ = b.ln.Close()
	}
}

// detect finds and checks the platform's mechanism.
func (m *Manager) detect() {
	switch m.opts.GOOS {
	case "darwin":
		path, err := m.opts.LookPath("sandbox-exec")
		if err != nil {
			if _, statErr := os.Stat("/usr/bin/sandbox-exec"); statErr != nil {
				m.availErr = errors.New("sandbox-exec was not found")
				return
			}
			path = "/usr/bin/sandbox-exec"
		}
		if err := m.probe([]string{path, "-p", "(version 1)(allow default)", "/usr/bin/true"}); err != nil {
			m.availErr = fmt.Errorf("sandbox-exec does not run here: %v", err)
			return
		}
		m.mech = "sandbox-exec (Seatbelt)"
	case "linux":
		bw, err := m.opts.LookPath("bwrap")
		if err != nil {
			m.availErr = errors.New("bubblewrap (bwrap) is not installed; install the bubblewrap package")
			return
		}
		so, err := m.opts.LookPath("socat")
		if err != nil {
			m.availErr = errors.New("socat is not installed; install the socat package (the sandbox's network relay)")
			return
		}
		args := []string{bw, "--ro-bind", "/", "/", "--dev", "/dev", "--unshare-net", "--unshare-pid"}
		if m.cfg.WeakerNested {
			args = append(args, "--bind", "/proc", "/proc")
		} else {
			args = append(args, "--proc", "/proc")
		}
		if err := m.probe(append(args, "--", "/bin/sh", "-c", "true")); err != nil {
			m.availErr = fmt.Errorf("bubblewrap cannot create a sandbox here (user namespaces may be restricted): %v", err)
			return
		}
		m.mech, m.bwrap, m.socat = "bubblewrap", bw, so
	default:
		m.availErr = fmt.Errorf("sandboxing is not supported on %s", m.opts.GOOS)
	}
}

func (m *Manager) probe(argv []string) error {
	if m.opts.Probe != nil {
		return m.opts.Probe(argv)
	}
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}

// network starts the proxy on first use and returns the ports sandboxed
// commands may reach: kiln's proxy, unless sandbox.network.httpProxyPort
// names the user's own, and the user's SOCKS port if set.
func (m *Manager) network() (httpPort, socksPort int, proxy *Proxy, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, 0, nil, errors.New("sandbox: session closed")
	}
	socksPort = m.cfg.SOCKSProxyPort
	if m.cfg.HTTPProxyPort > 0 {
		return m.cfg.HTTPProxyPort, socksPort, nil, nil
	}
	if m.proxy == nil && m.proxyErr == nil {
		p := NewProxy(m.cfg.AllowedDomains, m.cfg.DeniedDomains, m.cfg.StrictAllowlist)
		p.Decide = m.decideNetwork
		p.LookupIP = m.opts.LookupIP
		if err := p.Start(); err != nil {
			m.proxyErr = err
		} else {
			m.proxy = p
		}
	}
	if m.proxyErr != nil {
		return 0, 0, nil, fmt.Errorf("sandbox: starting the network proxy: %w", m.proxyErr)
	}
	return m.proxy.Port(), socksPort, m.proxy, nil
}

// tmpDir returns (creating it) the per-user directory sandboxed commands
// use as $TMPDIR. It must be a real directory this user owns, not a link
// someone planted.
func (m *Manager) tmpDir() (string, error) {
	dir := m.opts.TmpDir
	if dir == "" {
		// As Claude Code's default: /tmp/claude-{uid} on macOS, the
		// system temp directory on Linux.
		base := os.TempDir()
		if m.opts.GOOS == "darwin" {
			base = "/tmp"
		}
		dir = filepath.Join(base, "kiln-"+strconv.Itoa(os.Getuid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("sandbox: temp directory %s is not a private directory", dir)
	}
	if !ownedByMe(fi) {
		return "", fmt.Errorf("sandbox: temp directory %s belongs to another user", dir)
	}
	return realPath(dir), nil
}

// plan resolves the configuration for a command run in cwd.
func (m *Manager) plan(cwd string) (Plan, *Proxy, error) {
	httpPort, socksPort, proxy, err := m.network()
	if err != nil {
		return Plan{}, nil, err
	}
	tmp, err := m.tmpDir()
	if err != nil {
		return Plan{}, nil, err
	}
	return buildPlan(m.cfg, cwd, m.opts.Roots(), tmp, m.opts.Home, httpPort, socksPort), proxy, nil
}

// commandSandbox is one command's sandbox (execenv.CommandSandbox).
type commandSandbox struct {
	m     *Manager
	proxy *Proxy
	mark  int
}

// Wrap implements execenv.CommandSandbox.
func (c *commandSandbox) Wrap(shell, command, cwd string) (execenv.Wrapped, error) {
	p, proxy, err := c.m.plan(cwd)
	if err != nil {
		return execenv.Wrapped{}, err
	}
	c.proxy = proxy
	if proxy != nil {
		c.mark = proxy.Mark()
	}
	switch c.m.opts.GOOS {
	case "darwin":
		profile, err := seatbeltProfile(p)
		if err != nil {
			return execenv.Wrapped{}, err
		}
		return execenv.Wrapped{
			Argv:  []string{"/usr/bin/sandbox-exec", "-p", profile, shell, "-c", command},
			Env:   p.Env,
			Unset: p.Unset,
		}, nil
	case "linux":
		var bridges []bwrapBridge
		for _, port := range []int{p.HTTPProxyPort, p.SOCKSProxyPort} {
			if port == 0 {
				continue
			}
			sock, err := c.m.bridge(port, filepath.Dir(p.TmpDir))
			if err != nil {
				return execenv.Wrapped{}, err
			}
			bridges = append(bridges, bwrapBridge{Port: port, Socket: sock})
		}
		held, releaseHeld, err := c.m.placeholders(p)
		if err != nil {
			return execenv.Wrapped{}, err
		}
		p.Placeholders = held
		before := snapshotGitDirs(p.GitDirs)
		release := func() {
			releaseHeld()
			sweepGitDirs(p.GitDirs, before)
		}
		argv, err := bwrapArgs(p, c.m.bwrap, c.m.socat, shell, command, bridges)
		if err != nil {
			release()
			return execenv.Wrapped{}, err
		}
		return execenv.Wrapped{Argv: argv, Unset: p.Unset, Cleanup: release}, nil
	}
	return execenv.Wrapped{}, fmt.Errorf("sandboxing is not supported on %s", c.m.opts.GOOS)
}

// sandboxErrorMarkers are what a refused operation prints.
var sandboxErrorMarkers = []string{
	"Operation not permitted", "Read-only file system",
	"Could not resolve host", "Network is unreachable", "Temporary failure in name resolution",
	"blocked by the kiln sandbox",
}

// Explain implements execenv.CommandSandbox: after a failed run, it names
// the hosts the proxy refused during it and says what the sandbox allows
// and how to retry outside it, when the output suggests the sandbox
// refused something.
func (c *commandSandbox) Explain(output string, exitCode int) string {
	if exitCode == 0 {
		return ""
	}
	var events []BlockEvent
	if c.proxy != nil {
		events, _ = c.proxy.EventsSince(c.mark)
	}
	suspicious := len(events) > 0
	for _, mk := range sandboxErrorMarkers {
		if strings.Contains(output, mk) {
			suspicious = true
			break
		}
	}
	if !suspicious {
		return ""
	}
	var b strings.Builder
	b.WriteString("[kiln sandbox] This command ran inside the OS sandbox: it may write only to the workspace and $TMPDIR, and reach the network only through kiln's proxy for allowed hosts.")
	if len(events) > 0 {
		seen := map[string]bool{}
		var hosts []string
		for _, e := range events {
			h := net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) + " (" + e.Reason + ")"
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
		sort.Strings(hosts)
		b.WriteString("\nBlocked network access: " + strings.Join(hosts, "; ") + ".")
	}
	if c.m.cfg.AllowUnsandboxed {
		b.WriteString("\nIf the command needs access the sandbox does not give, run it again with dangerouslyDisableSandbox: true; the user is asked to approve running it outside the sandbox.")
	} else {
		b.WriteString("\nRunning commands outside the sandbox is disabled (sandbox.allowUnsandboxedCommands is false); ask the user to widen the sandbox settings instead.")
	}
	return b.String()
}

// bridge is a Unix-socket listener outside the Linux sandbox that relays
// each connection to a loopback port (bwrap.go).
type bridge struct {
	ln   net.Listener
	path string
}

func (m *Manager) bridge(port int, dir string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.bridges[port]; ok {
		return b.path, nil
	}
	sockDir, err := os.MkdirTemp(dir, "kiln-net-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(sockDir, "proxy-"+strconv.Itoa(port)+".sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go relay(c, "127.0.0.1:"+strconv.Itoa(port))
		}
	}()
	m.bridges[port] = &bridge{ln: ln, path: path}
	return path, nil
}

func relay(c net.Conn, target string) {
	defer c.Close()
	u, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer u.Close()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(u, c)
		close(done)
	}()
	_, _ = io.Copy(c, u)
	<-done
}
