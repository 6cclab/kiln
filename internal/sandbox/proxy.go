package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Proxy is the local HTTP proxy sandboxed commands reach the network
// through (code.claude.com/docs/en/sandboxing, "Network isolation"). The
// sandbox lets a command connect to nothing but this proxy's loopback
// port; the proxy decides each connection by the requested host:
//
//  1. a host in the deny list is refused;
//  2. a host in the allow list, or one approved earlier this session, is
//     allowed;
//  3. with a strict allowlist, anything else is refused;
//  4. otherwise Decide is asked (the permission mode, and in manual modes
//     the user), and with no Decide the host is refused.
//
// An allowed hostname that resolves only to local addresses (loopback,
// link-local, this machine's own) is refused unless that address is
// itself allowlisted; "localhost" and "*.localhost" may resolve to
// loopback. The proxy dials the address it checked, so a second lookup
// cannot swap it.
//
// It speaks CONNECT (HTTPS and any TCP tunnel) and plain HTTP requests in
// absolute form. It does not terminate TLS.
type Proxy struct {
	allow, deny []hostRule
	strict      bool

	// Decide is asked about a host no list settles. allow reports the
	// answer; always asks to keep allowing the host for the session.
	Decide func(ctx context.Context, host string, port int) (allow, always bool, err error)
	// LookupIP resolves a hostname; nil uses the system resolver.
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
	// LocalAddrs lists this machine's own addresses; nil reads the
	// interfaces.
	LocalAddrs func() []netip.Addr

	mu       sync.Mutex
	approved map[string]bool // host -> allowed for the session
	token    string          // the credential Userinfo carries
	events   []BlockEvent

	ln  net.Listener
	srv *http.Server
	// tunnels holds the hijacked CONNECT tunnels, which srv.Close does
	// not reach.
	tunnels tunnels
}

// BlockEvent is one connection the proxy refused.
type BlockEvent struct {
	Seq    int
	Host   string
	Port   int
	Reason string
}

// NewProxy builds a proxy for the given allow and deny lists.
func NewProxy(allowed, denied []string, strict bool) *Proxy {
	var tok [16]byte
	_, _ = rand.Read(tok[:])
	return &Proxy{
		allow:    parseHostRules(allowed, false),
		deny:     parseHostRules(denied, true),
		strict:   strict,
		approved: map[string]bool{},
		token:    hex.EncodeToString(tok[:]),
	}
}

// proxyUser is the user name in the proxy's credential; the password is
// the per-session token.
const proxyUser = "kiln"

// Userinfo is the credential sandboxed commands put in the proxy URL
// ("kiln:<token>"). The proxy serves only requests that carry it, so
// another process on this machine cannot use it, nor raise prompts
// through it.
func (p *Proxy) Userinfo() string { return proxyUser + ":" + p.token }

func (p *Proxy) authorized(r *http.Request) bool {
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(p.Userinfo()))
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(want)) == 1
}

// Start listens on a loopback port and serves until Close.
func (p *Proxy) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	p.ln = ln
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return nil
}

// Port is the loopback port the proxy listens on.
func (p *Proxy) Port() int {
	if p.ln == nil {
		return 0
	}
	return p.ln.Addr().(*net.TCPAddr).Port
}

// Close stops the proxy and every tunnel through it. http.Server.Close
// leaves hijacked connections alone, so the CONNECT tunnels are closed
// here, and Close returns only once their goroutines have finished.
func (p *Proxy) Close() error {
	if p.srv == nil {
		return nil
	}
	err := p.srv.Close()
	p.tunnels.closeAll()
	return err
}

// Allow adds a host to the session allowlist.
func (p *Proxy) Allow(host string) {
	p.mu.Lock()
	p.approved[strings.ToLower(host)] = true
	p.mu.Unlock()
}

// EventsSince returns the refusals recorded after seq, and the latest seq.
func (p *Proxy) EventsSince(seq int) ([]BlockEvent, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []BlockEvent
	for _, e := range p.events {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	return out, len(p.events)
}

// Mark returns the current event sequence number.
func (p *Proxy) Mark() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

func (p *Proxy) block(host string, port int, reason string) string {
	p.mu.Lock()
	p.events = append(p.events, BlockEvent{Seq: len(p.events) + 1, Host: host, Port: port, Reason: reason})
	p.mu.Unlock()
	return fmt.Sprintf("Connection to %s blocked by the kiln sandbox: %s", host, reason)
}

// errBlocked carries the refusal text sent back to the client.
type errBlocked struct{ msg string }

func (e errBlocked) Error() string { return e.msg }

// decide applies the lists, the session approvals and Decide.
func (p *Proxy) decide(ctx context.Context, host string, port int) error {
	if anyMatches(p.deny, host, port) {
		return errBlocked{p.block(host, port, "the host is in sandbox.network.deniedDomains")}
	}
	// A local destination — a loopback, link-local (169.254.169.254, the
	// cloud metadata endpoint), unspecified or own address, or a
	// localhost name — is reached only when an entry names it exactly:
	// never through "*", a session approval or bypass mode.
	if kind := p.localTarget(host); kind != "" {
		if p.explicitlyAllowed(host, port) {
			return nil
		}
		return errBlocked{p.block(host, port, "it is "+kind+"; allow it by its exact address in sandbox.network.allowedDomains")}
	}
	if anyMatches(p.allow, host, port) {
		return nil
	}
	p.mu.Lock()
	ok := p.approved[strings.ToLower(host)]
	p.mu.Unlock()
	if ok {
		return nil
	}
	if p.strict {
		return errBlocked{p.block(host, port, "the host is not in sandbox.network.allowedDomains")}
	}
	if p.Decide == nil {
		return errBlocked{p.block(host, port, "the host is not in sandbox.network.allowedDomains")}
	}
	allow, _, err := p.Decide(ctx, host, port)
	if err != nil {
		return errBlocked{p.block(host, port, "the network request could not be approved: "+err.Error())}
	}
	if !allow {
		return errBlocked{p.block(host, port, "the host is not in sandbox.network.allowedDomains and was not approved")}
	}
	// "Yes" allows the host for the rest of the session (Claude Code:
	// "allows the host for the rest of the current session"); "don't ask
	// again" also saves it, which Decide's caller does.
	p.Allow(host)
	return nil
}

// dial connects to host:port after decide allowed it, applying the
// local-address check to hostnames.
func (p *Proxy) dial(ctx context.Context, host string, port int) (net.Conn, error) {
	var d net.Dialer
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return d.DialContext(ctx, "tcp", netip.AddrPortFrom(addr.Unmap(), uint16(port)).String())
	}
	lookup := p.LookupIP
	if lookup == nil {
		lookup = func(ctx context.Context, h string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		}
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	// localhost names may resolve to loopback (Claude Code's rule), but
	// only when an entry names them exactly (decide).
	localOK := p.localTarget(host) != "" && p.explicitlyAllowed(host, port)
	locals := p.localAddrs()
	var permitted []netip.Addr
	var kind string
	for _, a := range addrs {
		a = a.Unmap()
		if k := localKind(a, locals); k != "" && !(localOK && a.IsLoopback()) && !p.ipAllowed(a, port) {
			kind = k
			continue
		}
		permitted = append(permitted, a)
	}
	if len(permitted) == 0 {
		if kind == "" {
			return nil, fmt.Errorf("no addresses for %s", host)
		}
		return nil, errBlocked{p.block(host, port, "resolved to "+kind)}
	}
	var lastErr error
	for _, a := range permitted {
		c, err := d.DialContext(ctx, "tcp", netip.AddrPortFrom(a, uint16(port)).String())
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// localTarget names why host (an IP literal, unmapped, or a localhost
// name) is local, or "".
func (p *Proxy) localTarget(host string) string {
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return localKind(addr.Unmap(), p.localAddrs())
	}
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return "a localhost name"
	}
	return ""
}

// explicitlyAllowed reports an allow entry other than "*" covering
// host:port.
func (p *Proxy) explicitlyAllowed(host string, port int) bool {
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return p.ipAllowed(addr, port)
	}
	for _, r := range p.allow {
		if !r.any && r.matches(host, port) {
			return true
		}
	}
	return false
}

func (p *Proxy) ipAllowed(a netip.Addr, port int) bool {
	for _, r := range p.allow {
		if r.matchesIP(a, port) {
			return true
		}
	}
	return false
}

func (p *Proxy) localAddrs() []netip.Addr {
	if p.LocalAddrs != nil {
		return p.LocalAddrs()
	}
	var out []netip.Addr
	ifaddrs, _ := net.InterfaceAddrs()
	for _, ia := range ifaddrs {
		if n, ok := ia.(*net.IPNet); ok {
			if a, ok := netip.AddrFromSlice(n.IP); ok {
				out = append(out, a.Unmap())
			}
		}
	}
	return out
}

// localKind names why an address counts as local, or "".
func localKind(a netip.Addr, own []netip.Addr) string {
	switch {
	case a.IsLoopback():
		return "a loopback address"
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast():
		return "a link-local address"
	case a.IsUnspecified():
		return "an unspecified address"
	}
	for _, o := range own {
		if o == a {
			return "an address of this machine"
		}
	}
	return ""
}

// ServeHTTP handles CONNECT tunnels and absolute-form HTTP requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="kiln sandbox"`)
		http.Error(w, "kiln sandbox proxy: this proxy serves only kiln's sandboxed commands", http.StatusProxyAuthRequired)
		return
	}
	r.Header.Del("Proxy-Authorization")
	if r.Method == http.MethodConnect {
		p.serveConnect(w, r)
		return
	}
	if r.URL == nil || !r.URL.IsAbs() || r.URL.Host == "" {
		http.Error(w, "kiln sandbox proxy: only proxy requests are served", http.StatusBadRequest)
		return
	}
	defPort := 80
	if r.URL.Scheme == "https" {
		defPort = 443
	}
	host, port, ok := splitHostPort(r.URL.Host, defPort)
	if !ok {
		http.Error(w, "kiln sandbox proxy: bad host", http.StatusBadRequest)
		return
	}
	if err := p.decide(r.Context(), host, port); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return p.dial(ctx, host, port)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
		out.Header.Del(h)
	}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		var b errBlocked
		if errors.As(err, &b) {
			http.Error(w, b.msg, http.StatusForbidden)
			return
		}
		http.Error(w, "kiln sandbox proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	host, port, ok := splitHostPort(r.Host, 443)
	if !ok {
		http.Error(w, "kiln sandbox proxy: bad CONNECT target", http.StatusBadRequest)
		return
	}
	if err := p.decide(r.Context(), host, port); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	upstream, err := p.dial(r.Context(), host, port)
	if err != nil {
		var b errBlocked
		if errors.As(err, &b) {
			http.Error(w, b.msg, http.StatusForbidden)
			return
		}
		http.Error(w, "kiln sandbox proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "kiln sandbox proxy: cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if !p.tunnels.begin(client, upstream) {
		return
	}
	defer p.tunnels.end(client, upstream)
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		// Bytes the client sent after the CONNECT header, already
		// buffered, go first.
		if n := buf.Reader.Buffered(); n > 0 {
			pending, _ := buf.Reader.Peek(n)
			_, _ = upstream.Write(pending)
		}
		_, _ = io.Copy(upstream, client)
		if c, ok := upstream.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
	}()
	_, _ = io.Copy(client, upstream)
	client.Close()
	upstream.Close()
	<-sent
}
