package sandbox

import (
	"net"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// hostRule is one entry of a domain list (sandbox.network.allowedDomains /
// deniedDomains, or a WebFetch(domain:…) rule), in the syntax Claude
// Code's settings reference documents for allowedDomains: a hostname, a
// "*.example.com" wildcard (subdomains only), a bare "*" (every host), an
// IPv4 literal or a bracketed IPv6 literal, each with an optional ":port"
// (no port: every port). A trailing dot is ignored.
type hostRule struct {
	any      bool
	wildcard bool   // "*.suffix": suffix holds ".example.com"
	host     string // lower-case hostname or canonical IP text
	ip       netip.Addr
	port     int // 0 = every port
}

// parseHostRules parses entries for an allow list (deny=false) or a deny
// list. An ambiguous unbracketed IPv6 entry ("::1:443") is read every way
// it can be for a deny list, so whatever was meant is blocked, and only as
// address-plus-port for an allow list, so the allowlist never widens past
// what was written; an entry that parses no way is dropped.
func parseHostRules(entries []string, deny bool) []hostRule {
	var out []hostRule
	for _, e := range entries {
		out = append(out, parseHostRule(e, deny)...)
	}
	return out
}

func parseHostRule(entry string, deny bool) []hostRule {
	s := strings.TrimSpace(entry)
	if hp, port, cut := strings.Cut(s, ":"); !strings.HasPrefix(s, "[") && strings.Count(s, ":") <= 1 {
		hp = canonicalName(hp)
		if cut {
			hp += ":" + port
		}
		s = hp
	}
	s = strings.ToLower(s)
	if s == "" {
		return nil
	}
	if s == "*" {
		return []hostRule{{any: true}}
	}
	// Bracketed IPv6, optional port.
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return nil
		}
		addr, err := netip.ParseAddr(s[1:end])
		if err != nil {
			return nil
		}
		port := 0
		if rest := s[end+1:]; rest != "" {
			p, ok := parsePort(strings.TrimPrefix(rest, ":"))
			if !ok || !strings.HasPrefix(rest, ":") {
				return nil
			}
			port = p
		}
		return []hostRule{ipRule(addr, port)}
	}
	if strings.Count(s, ":") > 1 {
		// Unbracketed IPv6: an address, or an address and a port.
		var withPort, bare []hostRule
		if i := strings.LastIndex(s, ":"); i > 0 {
			if addr, err := netip.ParseAddr(s[:i]); err == nil && addr.Is6() {
				if p, ok := parsePort(s[i+1:]); ok {
					withPort = []hostRule{ipRule(addr, p)}
				}
			}
		}
		if addr, err := netip.ParseAddr(s); err == nil {
			bare = []hostRule{ipRule(addr, 0)}
		}
		if deny {
			return append(withPort, bare...)
		}
		if withPort != nil {
			return withPort
		}
		return bare
	}
	host, port := s, 0
	if i := strings.LastIndex(s, ":"); i >= 0 {
		p, ok := parsePort(s[i+1:])
		if !ok {
			return nil
		}
		host, port = s[:i], p
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return []hostRule{ipRule(addr, port)}
	}
	if suffix, ok := strings.CutPrefix(host, "*."); ok {
		if suffix == "" || strings.Contains(suffix, "*") {
			return nil
		}
		return []hostRule{{wildcard: true, host: "." + suffix, port: port}}
	}
	if strings.ContainsAny(host, "*/ ") {
		return nil // a wildcard anywhere else matches nothing in the sandbox
	}
	return []hostRule{{host: host, port: port}}
}

func ipRule(addr netip.Addr, port int) hostRule {
	addr = addr.Unmap()
	return hostRule{ip: addr, host: addr.String(), port: port}
}

func parsePort(s string) (int, bool) {
	p, err := strconv.Atoi(s)
	if err != nil || p <= 0 || p > 65535 {
		return 0, false
	}
	return p, true
}

// matches reports whether the rule covers host (a hostname or an IP
// literal, without brackets) on port.
func (r hostRule) matches(host string, port int) bool {
	if r.port != 0 && r.port != port {
		return false
	}
	if r.any {
		return true
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return r.ip.IsValid() && r.ip == addr.Unmap()
	}
	if r.ip.IsValid() {
		return false
	}
	if r.wildcard {
		return strings.HasSuffix(host, r.host) && len(host) > len(r.host)
	}
	return host == r.host
}

// matchesIP reports whether an explicit IP entry covers addr on port (the
// local-address check's escape: an allowed name may resolve to a local
// address only when that address is itself allowlisted). "*" does not
// count: it names no address.
func (r hostRule) matchesIP(addr netip.Addr, port int) bool {
	return r.ip.IsValid() && r.ip == addr.Unmap() && (r.port == 0 || r.port == port)
}

func anyMatches(rules []hostRule, host string, port int) bool {
	for _, r := range rules {
		if r.matches(host, port) {
			return true
		}
	}
	return false
}

// splitHostPort splits "host:port" (or "[v6]:port"), defaulting the port
// when absent.
func splitHostPort(hostport string, defaultPort int) (string, int, bool) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		if strings.Contains(err.Error(), "missing port") {
			return canonicalHost(hostport), defaultPort, hostport != ""
		}
		return "", 0, false
	}
	p, ok := parsePort(portStr)
	if !ok || host == "" {
		return "", 0, false
	}
	return canonicalHost(host), p, true
}

// canonicalName puts a domain-list entry's host part (possibly "*." or
// "*") in the form canonicalHost gives hosts.
func canonicalName(h string) string {
	if h == "*" {
		return h
	}
	if rest, ok := strings.CutPrefix(h, "*."); ok {
		return "*." + canonicalHost(rest)
	}
	return canonicalHost(h)
}

// canonicalHost is the form a host is decided, shown and matched in: an
// IP literal unmapped from IPv4-in-IPv6 ("::ffff:127.0.0.1" is
// 127.0.0.1), a name lower-cased, without a trailing dot, and in its ASCII
// (punycode) form, so an internationalized name and its punycode spelling
// are one host.
func canonicalHost(h string) string {
	h = strings.Trim(h, "[]")
	if addr, err := netip.ParseAddr(h); err == nil {
		return addr.Unmap().String()
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if a, err := idna.Lookup.ToASCII(h); err == nil {
		return a
	}
	return h
}
