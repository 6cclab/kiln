package mcp

// OAuth 2.1 (authorization-code + PKCE) for http MCP servers whose config
// carries an `oauth` block. The go-sdk's AuthorizationCodeHandler does the
// protocol (protected-resource discovery, PKCE, token exchange/refresh); this
// file supplies the three things it can't: a preregistered client, a loopback
// redirect that captures the code from the browser, and token persistence so a
// sign-in survives across sessions. See docs/design-mcp-oauth.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/diag"
)

// authorizeTimeout bounds one interactive browser sign-in. It is deliberately
// far longer than the 30s MCP connect timeout, which would otherwise cancel a
// human mid-sign-in (design review P0 #1).
const authorizeTimeout = 5 * time.Minute

// defaultRedirectPath is the loopback redirect path used when the oauth config
// names none. It must match a redirect URI registered for the client at the
// authorization server, so a server whose IdP registered a different path must
// set OAuthConfig.RedirectPath.
const defaultRedirectPath = "/callback"

// buildOAuthHandler returns the go-sdk OAuthHandler for serverURL's oauth
// config, or an error if the config is unusable. headless (print mode / no
// terminal) makes an un-cached sign-in fail fast rather than block. notify, if
// set, is called once with a line telling the user to finish sign-in in the
// browser.
func buildOAuthHandler(serverURL string, cfg *OAuthConfig, headless bool, notify func(string)) (sdkauth.OAuthHandler, error) {
	if cfg.ClientID == "" {
		return nil, errors.New("oauth: clientId is required")
	}
	if cfg.CallbackPort <= 0 {
		return nil, errors.New("oauth: callbackPort is required")
	}
	path := cfg.RedirectPath
	if path == "" {
		path = defaultRedirectPath
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d%s", cfg.CallbackPort, path)
	store := newOAuthTokenStore()
	key := "mcp-oauth:" + serverURL

	hc := &sdkauth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: &oauthex.ClientCredentials{ClientID: cfg.ClientID},
		RedirectURL:         redirect,
		RequestRefreshToken: true,
		AuthorizationCodeFetcher: func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
			if headless {
				return nil, fmt.Errorf("oauth: %s needs an interactive sign-in; run kiln in a terminal and open %s", serverURL, args.URL)
			}
			return fetchAuthCode(ctx, cfg.CallbackPort, path, args.URL, notify)
		},
		// Persist every token (first exchange and each refresh), and record the
		// token endpoint so a later session can refresh without a new sign-in.
		NewTokenSource: func(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			store.save(key, oc, tok)
			return persisting{store: store, key: key, oc: oc, inner: oc.TokenSource(ctx, tok)}, nil
		},
	}
	if len(cfg.Scopes) > 0 {
		want := append([]string(nil), cfg.Scopes...)
		hc.ScopeFilter = func([]string) []string { return want }
	}
	// Seed from a stored token so an unexpired/ refreshable session skips the
	// browser entirely.
	if oc, tok := store.load(key); tok != nil {
		hc.InitialTokenSource = persisting{store: store, key: key, oc: oc, inner: oc.TokenSource(context.Background(), tok)}
	}
	return sdkauth.NewAuthorizationCodeHandler(hc)
}

// persisting wraps a TokenSource so each freshly minted token (initial or
// refreshed) is written back to the store.
type persisting struct {
	store *oauthTokenStore
	key   string
	oc    *oauth2.Config
	inner oauth2.TokenSource
}

func (p persisting) Token() (*oauth2.Token, error) {
	tok, err := p.inner.Token()
	if err != nil {
		return nil, err
	}
	p.store.save(p.key, p.oc, tok)
	return tok, nil
}

// fetchAuthCode opens url in the browser and captures the authorization code
// from the loopback redirect. The listener binds 127.0.0.1 only, serves the one
// redirect path, closes after the first valid response, and gives up after
// authorizeTimeout or ctx cancellation.
func fetchAuthCode(ctx context.Context, port int, path, url string, notify func(string)) (*sdkauth.AuthorizationResult, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("oauth: callback port %d unavailable: %w", port, err)
	}
	type result struct {
		res *sdkauth.AuthorizationResult
		err error
	}
	resCh := make(chan result, 1)
	var once sync.Once
	deliver := func(r result) { once.Do(func() { resCh <- r }) }

	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, "Sign-in failed: "+e, http.StatusBadRequest)
			deliver(result{err: fmt.Errorf("oauth: authorization error %q", e)})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "Missing code.", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "<html><body>Signed in. You can close this window.</body></html>")
		deliver(result{res: &sdkauth.AuthorizationResult{Code: code, State: q.Get("state"), Iss: q.Get("iss")}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Handler: mux}
	crash.Go(func() { _ = srv.Serve(ln) })
	defer srv.Close()

	if notify != nil {
		notify("Open your browser to finish signing in")
	}
	openBrowser(url)

	ctx, cancel := context.WithTimeout(ctx, authorizeTimeout)
	defer cancel()
	select {
	case r := <-resCh:
		return r.res, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("oauth: timed out waiting for browser sign-in")
	}
}

// openBrowser is a best-effort open of url; a failure is logged, not fatal (the
// user can open the URL from the notice). A var so tests can stub it.
var openBrowser = func(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	if err := exec.Command(cmd, append(args, url)...).Start(); err != nil {
		diag.L().Warn("oauth: couldn't open a browser", "error", err)
	}
}

// --- token persistence -------------------------------------------------------

// storedToken is one server's persisted OAuth token plus what a later session
// needs to refresh it without a new sign-in.
type storedToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	TokenType    string    `json:"token_type"`
	ClientID     string    `json:"client_id"`
	TokenURL     string    `json:"token_url"`
	AuthURL      string    `json:"auth_url"`
}

// oauthTokenStore is a 0600 JSON map, keyed per server, under ~/.harness. It is
// separate from credentials.json because an MCP token needs the token endpoint
// stored alongside it (credentials.json's shape has nowhere for that).
type oauthTokenStore struct {
	path string
	mu   sync.Mutex
}

func newOAuthTokenStore() *oauthTokenStore {
	home, _ := os.UserHomeDir()
	return &oauthTokenStore{path: filepath.Join(home, ".harness", "mcp-oauth.json")}
}

func (s *oauthTokenStore) readAll() map[string]storedToken {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return map[string]storedToken{}
	}
	var m map[string]storedToken
	if json.Unmarshal(data, &m) != nil || m == nil {
		return map[string]storedToken{}
	}
	return m
}

func (s *oauthTokenStore) save(key string, oc *oauth2.Config, tok *oauth2.Token) {
	if tok == nil || tok.AccessToken == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readAll()
	st := storedToken{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
		TokenType:    tok.TokenType,
	}
	if oc != nil {
		st.ClientID, st.TokenURL, st.AuthURL = oc.ClientID, oc.Endpoint.TokenURL, oc.Endpoint.AuthURL
	}
	// Keep an existing refresh token if a refresh response omitted it.
	if st.RefreshToken == "" {
		if prev, ok := m[key]; ok {
			st.RefreshToken = prev.RefreshToken
		}
	}
	m[key] = st
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		diag.L().Warn("oauth: couldn't create token store dir", "error", err)
		return
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		diag.L().Warn("oauth: couldn't write token store", "error", err)
	}
}

// load returns a refreshing oauth2.Config + token for key, or nils if none is
// stored. The returned Config carries the token endpoint, so the SDK's
// TokenSource can refresh across sessions.
func (s *oauthTokenStore) load(key string) (*oauth2.Config, *oauth2.Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.readAll()[key]
	if !ok || st.AccessToken == "" {
		return nil, nil
	}
	oc := &oauth2.Config{
		ClientID: st.ClientID,
		Endpoint: oauth2.Endpoint{AuthURL: st.AuthURL, TokenURL: st.TokenURL},
	}
	tok := &oauth2.Token{
		AccessToken:  st.AccessToken,
		RefreshToken: st.RefreshToken,
		Expiry:       st.Expiry,
		TokenType:    st.TokenType,
	}
	return oc, tok
}
