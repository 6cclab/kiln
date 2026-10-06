package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/crash"
)

// Anthropic OAuth flow (Claude Pro/Max), ported from
// pi-ai/dist/auth/oauth/anthropic.js.

// anthropicClientID decodes to "9d1c250a-e61b-44d9-88ed-5944d1962f5e", matching
// pi's atob(CLIENT_ID) exactly (verified: `echo <b64> | base64 -d`).
const anthropicClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

const (
	anthropicAuthorizeURL        = "https://claude.ai/oauth/authorize"
	defaultAnthropicCallbackPort = 53692
	anthropicCallbackPath        = "/callback"
	anthropicScopes              = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
)

// anthropicTokenURL is a var, not a const, so tests can point it at an
// httptest server instead of platform.claude.com.
var anthropicTokenURL = "https://platform.claude.com/v1/oauth/token"

// SetTokenURLForTesting points the Anthropic OAuth token endpoint at url
// (an httptest server) for the duration of a test, returning a restore
// function. For tests outside this package (e.g. internal/auth/login,
// internal/provider/builtin) that need to exercise LoginAnthropic/
// RefreshAnthropicToken without a real network call.
func SetTokenURLForTesting(url string) (restore func()) {
	orig := anthropicTokenURL
	anthropicTokenURL = url
	return func() { anthropicTokenURL = orig }
}

// anthropicCallbackHost honors PI_OAUTH_CALLBACK_HOST, matching pi's
// getProviderEnvValue("PI_OAUTH_CALLBACK_HOST").
func anthropicCallbackHost() string {
	if h := os.Getenv("PI_OAUTH_CALLBACK_HOST"); h != "" {
		return h
	}
	return "127.0.0.1"
}

func anthropicRedirectURI(port int) string {
	return fmt.Sprintf("http://localhost:%d%s", port, anthropicCallbackPath)
}

// AnthropicLoginOption configures LoginAnthropic.
type AnthropicLoginOption func(*anthropicLoginConfig)

type anthropicLoginConfig struct {
	port int
}

// WithCallbackPort overrides the local callback server's port (default
// 53692, the same port pi's CLI binds). Tests use this so the local OAuth
// callback server does not need the real port.
func WithCallbackPort(port int) AnthropicLoginOption {
	return func(c *anthropicLoginConfig) { c.port = port }
}

// AnthropicOAuth is the harness's login/refresh/toAuth surface for Anthropic
// subscription auth, mirroring pi's exported `anthropicOAuth`.
var AnthropicOAuth = struct {
	Name           string
	IsSubscription bool
}{
	Name:           "Anthropic (Claude Pro/Max)",
	IsSubscription: true,
}

// AnthropicToken is the token half of an OAuthCredential (refresh/access are
// stored verbatim in auth.OAuthCredential; this is the intermediate shape
// the token endpoint returns).
type AnthropicToken struct {
	Refresh string
	Access  string
	// Expires is epoch milliseconds, already reduced by the 5-minute margin
	// pi bakes in at store time.
	Expires int64
}

// AnthropicToAuth derives request auth (a bearer API key) from a stored
// OAuth credential, matching pi's toAuth(credential) => { apiKey: access }.
func AnthropicToAuth(access string) string { return access }

// authCode is what the callback server or manual-code fallback produces.
type authCode struct {
	code, state string
}

// callbackServer is the local HTTP server pi starts to catch the OAuth
// redirect.
type callbackServer struct {
	srv         *http.Server
	ln          net.Listener
	redirectURI string
	resultCh    chan *authCode // buffered 1; nil sent means "cancelled"
	once        sync.Once
}

func startAnthropicCallbackServer(expectedState string, port int) (*callbackServer, error) {
	host := anthropicCallbackHost()
	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	cs := &callbackServer{ln: ln, redirectURI: anthropicRedirectURI(port), resultCh: make(chan *authCode, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc(anthropicCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body>Anthropic authentication did not complete. Error: %s</body></html>", e)
			return
		}
		code := q.Get("code")
		state := q.Get("state")
		if code == "" || state == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body>Missing code or state parameter.</body></html>")
			return
		}
		if state != expectedState {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body>State mismatch.</body></html>")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body>Anthropic authentication completed. You can close this window.</body></html>")
		cs.once.Do(func() { cs.resultCh <- &authCode{code: code, state: state} })
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != anthropicCallbackPath {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "<html><body>Callback route not found.</body></html>")
		}
	})
	cs.srv = &http.Server{Handler: mux}
	crash.Go(func() { _ = cs.srv.Serve(ln) })
	return cs, nil
}

// waitForCode blocks until the callback fires, ctx is cancelled, or cancel()
// is called directly. Returns nil on cancellation.
func (cs *callbackServer) waitForCode(ctx context.Context) *authCode {
	select {
	case r := <-cs.resultCh:
		return r
	case <-ctx.Done():
		cs.cancel()
		return nil
	}
}

func (cs *callbackServer) cancel() {
	cs.once.Do(func() { cs.resultCh <- nil })
}

func (cs *callbackServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = cs.srv.Shutdown(ctx)
}

// Interaction is the login prompt/notify surface, matching src/auth's
// ProviderAuthInteraction subset AnthropicLogin needs.
type Interaction interface {
	// NotifyAuthURL is called once with the URL to open and instructions.
	NotifyAuthURL(url, instructions string)
	// NotifyProgress reports a short status line.
	NotifyProgress(message string)
	// PromptManualCode blocks for a pasted code or redirect URL, honoring
	// ctx cancellation (the callback server winning the race cancels this).
	PromptManualCode(ctx context.Context, message, placeholder string) (string, error)
}

func parseAuthorizationInput(input string) (code, state string) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", ""
	}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" && u.Query().Has("code") {
		q := u.Query()
		return q.Get("code"), q.Get("state")
	}
	if strings.Contains(value, "#") {
		parts := strings.SplitN(value, "#", 2)
		return parts[0], parts[1]
	}
	if strings.Contains(value, "code=") {
		q, err := url.ParseQuery(value)
		if err == nil {
			return q.Get("code"), q.Get("state")
		}
	}
	return value, ""
}

// LoginAnthropic runs the PKCE authorization-code flow: starts a local
// callback server, prints/opens the authorize URL, races the callback
// against a manual-code paste, then exchanges the code for tokens.
//
// ctx cancellation aborts the whole flow (matching interaction.signal in the
// TS version).
func LoginAnthropic(ctx context.Context, interaction Interaction, opts ...AnthropicLoginOption) (AnthropicToken, error) {
	cfg := anthropicLoginConfig{port: defaultAnthropicCallbackPort}
	for _, opt := range opts {
		opt(&cfg)
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return AnthropicToken{}, err
	}
	// pi sends `state: verifier` -- the verifier doubles as the OAuth state
	// parameter. Preserved here even though it reuses a secret as a
	// (short-lived, single-use) nonce, to stay byte-for-byte compatible with
	// what platform.claude.com expects back.
	state := pkce.Verifier

	cs, err := startAnthropicCallbackServer(state, cfg.port)
	if err != nil {
		return AnthropicToken{}, fmt.Errorf("start OAuth callback server: %w", err)
	}
	defer cs.close()

	authParams := url.Values{}
	authParams.Set("code", "true")
	authParams.Set("client_id", anthropicClientID)
	authParams.Set("response_type", "code")
	authParams.Set("redirect_uri", cs.redirectURI)
	authParams.Set("scope", anthropicScopes)
	authParams.Set("code_challenge", pkce.Challenge)
	authParams.Set("code_challenge_method", "S256")
	authParams.Set("state", state)

	interaction.NotifyAuthURL(
		anthropicAuthorizeURL+"?"+authParams.Encode(),
		"Complete login in your browser. If the browser is on another machine, paste the final redirect URL here.",
	)

	manualCtx, cancelManual := context.WithCancel(ctx)
	defer cancelManual()

	type manualResult struct {
		input string
		err   error
	}
	manualCh := make(chan manualResult, 1)
	crash.Go(func() {
		input, err := interaction.PromptManualCode(manualCtx,
			"Complete login in your browser, or paste the authorization code / redirect URL here:",
			cs.redirectURI)
		manualCh <- manualResult{input, err}
		cs.cancel()
	})

	result := cs.waitForCode(ctx)

	var code, gotState string
	var manualErr error
	if result != nil {
		code, gotState = result.code, result.state
	} else {
		select {
		case m := <-manualCh:
			if m.err != nil {
				manualErr = m.err
			} else if m.input != "" {
				c, s := parseAuthorizationInput(m.input)
				if s != "" && s != state {
					return AnthropicToken{}, fmt.Errorf("OAuth state mismatch")
				}
				code = c
				if s != "" {
					gotState = s
				} else {
					gotState = state
				}
			}
		case <-ctx.Done():
			return AnthropicToken{}, ctx.Err()
		}
	}
	if manualErr != nil {
		return AnthropicToken{}, manualErr
	}
	if code == "" {
		return AnthropicToken{}, fmt.Errorf("missing authorization code")
	}
	if gotState == "" {
		return AnthropicToken{}, fmt.Errorf("missing OAuth state")
	}

	interaction.NotifyProgress("Exchanging authorization code for tokens...")
	return exchangeAnthropicCode(ctx, code, gotState, pkce.Verifier, cs.redirectURI)
}

type anthropicTokenResponse struct {
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func postAnthropicJSON(ctx context.Context, url string, body map[string]any) (anthropicTokenResponse, error) {
	var out anthropicTokenResponse
	payload, err := json.Marshal(body)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("HTTP request failed: %w; url=%s", err, url)
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("HTTP request failed. status=%d; url=%s; body=%s", resp.StatusCode, url, string(buf))
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		return out, fmt.Errorf("token response returned invalid JSON. url=%s; body=%s; err=%w", url, string(buf), err)
	}
	return out, nil
}

// fiveMinuteMarginMs is the refresh margin pi bakes into the stored expiry so
// a token already inside its last 5 minutes of life is treated as expired
// and refreshed proactively.
const fiveMinuteMarginMs = 5 * 60 * 1000

func exchangeAnthropicCode(ctx context.Context, code, state, verifier, redirectURI string) (AnthropicToken, error) {
	resp, err := postAnthropicJSON(ctx, anthropicTokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     anthropicClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	})
	if err != nil {
		return AnthropicToken{}, fmt.Errorf("token exchange request failed: %w", err)
	}
	return AnthropicToken{
		Refresh: resp.RefreshToken,
		Access:  resp.AccessToken,
		Expires: time.Now().UnixMilli() + resp.ExpiresIn*1000 - fiveMinuteMarginMs,
	}, nil
}

// RefreshAnthropicToken exchanges a refresh token for a new access token,
// matching pi's refreshAnthropicToken.
func RefreshAnthropicToken(ctx context.Context, refreshToken string) (AnthropicToken, error) {
	resp, err := postAnthropicJSON(ctx, anthropicTokenURL, map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     anthropicClientID,
		"refresh_token": refreshToken,
	})
	if err != nil {
		return AnthropicToken{}, fmt.Errorf("anthropic token refresh request failed: %w", err)
	}
	return AnthropicToken{
		Refresh: resp.RefreshToken,
		Access:  resp.AccessToken,
		Expires: time.Now().UnixMilli() + resp.ExpiresIn*1000 - fiveMinuteMarginMs,
	}, nil
}
