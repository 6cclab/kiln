package sandbox

import "testing"

// Internationalized names and their punycode spellings are one host, in
// allow and deny lists alike; IPv4-in-IPv6 spellings are their IPv4
// address.
func TestHostCanonicalForms(t *testing.T) {
	deny := parseHostRules([]string{"bücher.example", "*.münchen.example"}, true)
	for _, h := range []string{"xn--bcher-kva.example", "BÜCHER.example.", "a.xn--mnchen-3ya.example"} {
		if !anyMatches(deny, canonicalHost(h), 443) {
			t.Errorf("deny list missed %q", h)
		}
	}
	allow := parseHostRules([]string{"xn--bcher-kva.example:443"}, false)
	if !anyMatches(allow, canonicalHost("bücher.example"), 443) {
		t.Error("allow list missed the Unicode spelling of a punycode entry")
	}
	if got := canonicalHost("[::ffff:127.0.0.1]"); got != "127.0.0.1" {
		t.Errorf("canonicalHost(v4-mapped) = %q", got)
	}
	if h, p, ok := splitHostPort("[::ffff:10.0.0.1]:8080", 80); !ok || h != "10.0.0.1" || p != 8080 {
		t.Errorf("splitHostPort = %q %d %v", h, p, ok)
	}
}
