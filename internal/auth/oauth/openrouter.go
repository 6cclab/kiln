package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/crash"
)

// OpenRouter OAuth flow, ported from pi-ai/dist/auth/oauth/openrouter.js.
//
// Unlike every other flow in this package, OpenRouter's authorization-code
// exchange does not return an access/refresh token pair: it returns a
// permanent, user-controlled API key. pi still boxes this in its Credential
// union as `{ type: "oauth", access: key, refresh: "", expires:
// Number.MAX_SAFE_INTEGER }`; the harness's login.go instead stores it as a
// plain api_key credential (per this phase's brief), since that is what it
// actually is and refresh is a no-op.

const openrouterLoginTimeout = 5 * time.Minute

var openrouterAuthorizeURL = "https://openrouter.ai/auth"
var openrouterTokenURL = "https://openrouter.ai/api/v1/auth/keys"

// SetOpenRouterURLsForTesting points OpenRouter's authorize/token endpoints
// at an httptest server for the duration of a test.
func SetOpenRouterURLsForTesting(authorizeURL, tokenURL string) (restore func()) {
	origAuthorize, origToken := openrouterAuthorizeURL, openrouterTokenURL
	openrouterAuthorizeURL = authorizeURL
	openrouterTokenURL = tokenURL
	return func() { openrouterAuthorizeURL, openrouterTokenURL = origAuthorize, origToken }
}

func openrouterCallbackHost() string {
	if h := os.Getenv("PI_OAUTH_CALLBACK_HOST"); h != "" {
		return h
	}
	return "127.0.0.1"
}

func exchangeOpenRouterCode(ctx context.Context, code, verifier string) (string, error) {
	status, body, raw, err := postJSON(ctx, openrouterTokenURL, map[string]any{
		"code":                  code,
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	}, nil)
	if err != nil {
		return "", fmt.Errorf("OpenRouter OAuth token exchange failed: %w", err)
	}
	if status < 200 || status >= 300 {
		detail := openrouterErrorDetail(body)
		if detail != "" {
			return "", fmt.Errorf("OpenRouter OAuth key exchange failed (HTTP %d): %s", status, detail)
		}
		return "", fmt.Errorf("OpenRouter OAuth key exchange failed (HTTP %d)", status)
	}
	key, ok := str(body, "key")
	if !ok {
		_ = raw
		return "", fmt.Errorf(`OpenRouter OAuth response carries no "key"`)
	}
	return key, nil
}

func openrouterErrorDetail(body map[string]any) string {
	if v, ok := str(body, "error_description"); ok {
		return v
	}
	if v, ok := str(body, "message"); ok {
		return v
	}
	if v, ok := str(body, "error"); ok {
		return v
	}
	if e, ok := body["error"].(map[string]any); ok {
		if v, ok := str(e, "message"); ok {
			return v
		}
	}
	return ""
}

type openrouterCallbackServer struct {
	srv         *http.Server
	ln          net.Listener
	callbackURL string
	resultCh    chan *string // nil result means "no code, cancelled"
	once        sync.Once
}

func startOpenRouterCallbackServer(callbackPath, verifier string) (*openrouterCallbackServer, error) {
	host := openrouterCallbackHost()
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	cs := &openrouterCallbackServer{ln: ln, callbackURL: fmt.Sprintf("http://%s:%d%s", host, port, callbackPath), resultCh: make(chan *string, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body>OpenRouter authorization was denied: %s</body></html>", e)
			return
		}
		code := q.Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body>OpenRouter returned no authorization code.</body></html>")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body>Signed in to OpenRouter. You may now close this page.</body></html>")
		cs.once.Do(func() { cs.resultCh <- &code })
	})
	cs.srv = &http.Server{Handler: mux}
	crash.Go(func() { _ = cs.srv.Serve(ln) })
	return cs, nil
}

func (cs *openrouterCallbackServer) waitForCode(ctx context.Context) *string {
	select {
	case r := <-cs.resultCh:
		return r
	case <-ctx.Done():
		cs.cancel()
		return nil
	}
}

func (cs *openrouterCallbackServer) cancel() {
	cs.once.Do(func() { cs.resultCh <- nil })
}

func (cs *openrouterCallbackServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = cs.srv.Shutdown(ctx)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// LoginOpenRouter runs the PKCE flow against openrouter.ai/auth and exchanges
// the resulting code for a permanent API key via POST
// api/v1/auth/keys, matching pi's loginOpenRouter. The local callback server
// binds an ephemeral port (as pi's does), so there is nothing to configure
// for tests beyond the token/authorize URLs.
func LoginOpenRouter(ctx context.Context, ia FlowInteraction) (string, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return "", err
	}
	suffix, err := randomHex(16)
	if err != nil {
		return "", err
	}
	callbackPath := "/oauth/callback/" + suffix

	cs, err := startOpenRouterCallbackServer(callbackPath, pkce.Verifier)
	if err != nil {
		return "", fmt.Errorf("start OAuth callback server: %w", err)
	}
	defer cs.close()

	loginCtx, cancel := context.WithTimeout(ctx, openrouterLoginTimeout)
	defer cancel()

	params := url.Values{}
	params.Set("callback_url", cs.callbackURL)
	params.Set("code_challenge", pkce.Challenge)
	params.Set("code_challenge_method", "S256")

	ia.NotifyProgress(fmt.Sprintf("Listening for OpenRouter OAuth callback on %s", cs.callbackURL))
	ia.NotifyAuthURL(openrouterAuthorizeURL+"?"+params.Encode(),
		"Complete sign-in in your browser. If the browser is on another machine, paste the final redirect URL here.")

	manualCtx, cancelManual := context.WithCancel(loginCtx)
	defer cancelManual()
	type manualResult struct {
		input string
		err   error
	}
	manualCh := make(chan manualResult, 1)
	crash.Go(func() {
		input, err := ia.PromptManualCode(manualCtx,
			"Complete sign-in in your browser, or paste the authorization code / redirect URL here:", cs.callbackURL)
		manualCh <- manualResult{input, err}
		cs.cancel()
	})

	code := cs.waitForCode(loginCtx)
	var manualErr error
	var resolvedCode string
	if code != nil {
		resolvedCode = *code
	} else {
		select {
		case m := <-manualCh:
			if m.err != nil {
				manualErr = m.err
			} else if m.input != "" {
				resolvedCode = parseOpenRouterManualInput(m.input)
			}
		case <-loginCtx.Done():
			return "", loginCtx.Err()
		}
	}
	if manualErr != nil {
		return "", manualErr
	}
	if resolvedCode == "" {
		return "", fmt.Errorf("missing authorization code")
	}
	ia.NotifyProgress("Exchanging authorization code for an API key...")
	return exchangeOpenRouterCode(loginCtx, resolvedCode, pkce.Verifier)
}

func parseOpenRouterManualInput(input string) string {
	if u, err := url.Parse(input); err == nil && u.Query().Has("code") {
		return u.Query().Get("code")
	}
	if q, err := url.ParseQuery(input); err == nil && q.Has("code") {
		return q.Get("code")
	}
	return input
}
