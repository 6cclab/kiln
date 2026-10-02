package sandbox

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

// Local destinations — loopback, unspecified, link-local (the cloud
// metadata address), v4-mapped spellings of them, localhost names — are
// reached only through an entry naming them exactly, never through "*" or
// an approval (bypass mode approves everything, so Decide returning yes
// stands in for it).
func TestProxyLocalTargets(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer target.Close()
	port := target.Listener.Addr().(*net.TCPAddr).Port

	get := func(p *Proxy, hostport string) int {
		proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", p.Port()))
		c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
		resp, err := c.Get("http://" + hostport + "/")
		if err != nil {
			return 0
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	start := func(allowed []string) (*Proxy, *int) {
		p := NewProxy(allowed, nil, false)
		asked := 0
		p.Decide = func(context.Context, string, int) (bool, bool, error) { asked++; return true, false, nil }
		p.LookupIP = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		if err := p.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		return p, &asked
	}

	p, asked := start([]string{"*"})
	for _, h := range []string{
		fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("0.0.0.0:%d", port),
		fmt.Sprintf("[::ffff:127.0.0.1]:%d", port), fmt.Sprintf("foo.localhost:%d", port),
		fmt.Sprintf("localhost:%d", port), "169.254.169.254:80",
	} {
		if code := get(p, h); code != http.StatusForbidden {
			t.Errorf("%s through \"*\" and approvals: status %d, want 403", h, code)
		}
	}
	if *asked != 0 {
		t.Errorf("a local destination was put to Decide %d times", *asked)
	}

	p, _ = start([]string{fmt.Sprintf("127.0.0.1:%d", port), "dev.localhost"})
	if code := get(p, fmt.Sprintf("127.0.0.1:%d", port)); code != 200 {
		t.Errorf("an exactly allowed IP: %d", code)
	}
	if code := get(p, fmt.Sprintf("[::ffff:127.0.0.1]:%d", port)); code != 200 {
		t.Errorf("the v4-mapped spelling of an allowed IP: %d", code)
	}
	if code := get(p, fmt.Sprintf("dev.localhost:%d", port)); code != 200 {
		t.Errorf("an exactly allowed localhost name: %d", code)
	}
	if code := get(p, fmt.Sprintf("other.localhost:%d", port)); code != http.StatusForbidden {
		t.Errorf("an unlisted localhost name: %d", code)
	}
	if code := get(p, fmt.Sprintf("127.0.0.1:%d", port+1)); code != http.StatusForbidden {
		t.Errorf("an allowed IP on another port: %d", code)
	}
}
