package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// A server's oauth block parses off the manifest/.mcp.json into ServerConfig
// and survives ExpandConfig.
func TestServerConfigOAuthParse(t *testing.T) {
	raw := `{"type":"http","url":"https://studio/mcp","oauth":{"clientId":"cid","callbackPort":43123,"scopes":["openid","groups"]}}`
	var cfg ServerConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.OAuth == nil {
		t.Fatal("oauth block not parsed")
	}
	got := ExpandConfig(cfg)
	if got.OAuth.ClientID != "cid" || got.OAuth.CallbackPort != 43123 || len(got.OAuth.Scopes) != 2 {
		t.Errorf("oauth = %+v, want cid/43123/2 scopes", got.OAuth)
	}
}

func TestBuildOAuthHandlerValidation(t *testing.T) {
	if _, err := buildOAuthHandler("https://x/mcp", &OAuthConfig{CallbackPort: 1}, false, nil); err == nil {
		t.Error("missing clientId should error")
	}
	if _, err := buildOAuthHandler("https://x/mcp", &OAuthConfig{ClientID: "c"}, false, nil); err == nil {
		t.Error("missing callbackPort should error")
	}
	if h, err := buildOAuthHandler("https://x/mcp", &OAuthConfig{ClientID: "c", CallbackPort: 45999}, false, nil); err != nil || h == nil {
		t.Errorf("valid config: handler=%v err=%v", h, err)
	}
}

// fetchAuthCode binds the loopback port, opens the (stubbed) browser, and
// returns the code/state/iss the redirect carries.
func TestFetchAuthCode(t *testing.T) {
	const port = 45931
	var opened string
	orig := openBrowser
	openBrowser = func(url string) {
		opened = url
		// Simulate the IdP redirecting the browser back to the loopback.
		go func() {
			var resp *http.Response
			var err error
			for i := 0; i < 3000; i++ { // wait for the listener
				resp, err = http.Get("http://127.0.0.1:" + "45931" + "/callback?code=abc&state=xyz&iss=https://idp")
				if err == nil {
					resp.Body.Close()
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			_ = err
		}()
	}
	defer func() { openBrowser = orig }()

	res, err := fetchAuthCode(context.Background(), port, "/callback", "https://idp/authorize?x=1", nil)
	if err != nil {
		t.Fatalf("fetchAuthCode: %v", err)
	}
	if opened != "https://idp/authorize?x=1" {
		t.Errorf("browser opened %q, want the authorize URL", opened)
	}
	if res.Code != "abc" || res.State != "xyz" || res.Iss != "https://idp" {
		t.Errorf("result = %+v, want code=abc state=xyz iss=https://idp", res)
	}
}

// An error redirect (user denied) fails the fetch rather than hanging.
func TestFetchAuthCode_ErrorRedirect(t *testing.T) {
	const port = 45932
	orig := openBrowser
	openBrowser = func(string) {
		go func() {
			for i := 0; i < 3000; i++ {
				if r, err := http.Get("http://127.0.0.1:45932/callback?error=access_denied"); err == nil {
					r.Body.Close()
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	defer func() { openBrowser = orig }()
	if _, err := fetchAuthCode(context.Background(), port, "/callback", "https://idp", nil); err == nil {
		t.Error("an error redirect should fail the fetch")
	}
}

// The token store round-trips a token + its endpoint, writes 0600, and keeps a
// prior refresh token when a refresh omits one.
func TestOAuthTokenStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s := newOAuthTokenStore()

	oc := &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: "https://idp/auth", TokenURL: "https://idp/token"}}
	tok := &oauth2.Token{AccessToken: "acc", RefreshToken: "ref", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour).Round(time.Second)}
	s.save("k", oc, tok)

	// 0600 on disk.
	fi, err := os.Stat(filepath.Join(dir, ".harness", "mcp-oauth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
	}

	gotCfg, gotTok := s.load("k")
	if gotTok.AccessToken != "acc" || gotTok.RefreshToken != "ref" {
		t.Errorf("loaded token = %+v", gotTok)
	}
	if gotCfg.ClientID != "cid" || gotCfg.Endpoint.TokenURL != "https://idp/token" {
		t.Errorf("loaded cfg = %+v, want the token endpoint for cross-session refresh", gotCfg)
	}

	// A refresh with no new refresh token keeps the old one.
	s.save("k", oc, &oauth2.Token{AccessToken: "acc2", TokenType: "Bearer"})
	if _, t2 := s.load("k"); t2.RefreshToken != "ref" {
		t.Errorf("refresh token not preserved across a refresh: %q", t2.RefreshToken)
	}

	if cfg, tk := s.load("absent"); cfg != nil || tk != nil {
		t.Error("load of an absent key should be nil,nil")
	}
}

// persisting writes each minted token back to the store.
func TestPersistingTokenSource(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s := newOAuthTokenStore()
	oc := &oauth2.Config{ClientID: "c", Endpoint: oauth2.Endpoint{TokenURL: "https://idp/token"}}
	src := persisting{store: s, key: "k", oc: oc, inner: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "live", TokenType: "Bearer"})}
	if _, err := src.Token(); err != nil {
		t.Fatal(err)
	}
	if _, tok := s.load("k"); tok == nil || tok.AccessToken != "live" {
		t.Errorf("token not persisted on mint: %+v", tok)
	}
}
