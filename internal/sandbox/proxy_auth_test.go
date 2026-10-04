package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The proxy serves only requests carrying the session's credential:
// another process on this machine gets 407 and never reaches a decision
// (so it cannot raise approval prompts either).
func TestProxyRequiresCredential(t *testing.T) {
	p := NewProxy([]string{"*"}, nil, false)
	asked := 0
	p.Decide = func(context.Context, string, int) (bool, bool, error) { asked++; return true, false, nil }
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	try := func(userinfo string) int {
		u := fmt.Sprintf("http://127.0.0.1:%d", p.Port())
		if userinfo != "" {
			u = fmt.Sprintf("http://%s@127.0.0.1:%d", userinfo, p.Port())
		}
		proxyURL, _ := url.Parse(u)
		c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
		resp, err := c.Get("http://example.invalid/")
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, ui := range []string{"", "kiln:wrong", "other:" + strings.TrimPrefix(p.Userinfo(), "kiln:")} {
		if code := try(ui); code != http.StatusProxyAuthRequired {
			t.Errorf("credential %q: status %d, want 407", ui, code)
		}
	}
	if asked != 0 {
		t.Errorf("an unauthenticated request reached Decide %d times", asked)
	}
	if NewProxy(nil, nil, false).Userinfo() == p.Userinfo() {
		t.Error("two proxies share a credential")
	}
}
