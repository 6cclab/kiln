package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenAI Codex (ChatGPT Plus/Pro subscription) OAuth flow, ported from
// pi-ai/dist/auth/oauth/openai-codex.js.

const (
	openaiCodexClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
	openaiCodexAuthBase       = "https://auth.openai.com"
	openaiCodexCallbackPath   = "/auth/callback"
	defaultOpenAICodexPort    = 1455
	openaiCodexScope          = "openid profile email offline_access"
	openaiCodexJWTClaimPath   = "https://api.openai.com/auth"
	openaiCodexDeviceTimeoutS = 15 * 60
)

// The following are vars, not consts, so tests can point them at an
// httptest server.
var (
	openaiCodexAuthorizeURL      = openaiCodexAuthBase + "/oauth/authorize"
	openaiCodexTokenURL          = openaiCodexAuthBase + "/oauth/token"
	openaiCodexDeviceUserCodeURL = openaiCodexAuthBase + "/api/accounts/deviceauth/usercode"
	openaiCodexDeviceTokenURL    = openaiCodexAuthBase + "/api/accounts/deviceauth/token"
	openaiCodexDeviceVerifyURI   = openaiCodexAuthBase + "/codex/device"
	openaiCodexDeviceRedirectURI = openaiCodexAuthBase + "/deviceauth/callback"
)

// SetOpenAICodexURLsForTesting points every OpenAI Codex OAuth endpoint at
// base (an httptest server) for the duration of a test, returning a restore
// function.
func SetOpenAICodexURLsForTesting(base string) (restore func()) {
	origAuthorize, origToken, origUserCode, origDeviceToken, origVerify, origDeviceRedirect :=
		openaiCodexAuthorizeURL, openaiCodexTokenURL, openaiCodexDeviceUserCodeURL, openaiCodexDeviceTokenURL, openaiCodexDeviceVerifyURI, openaiCodexDeviceRedirectURI
	openaiCodexAuthorizeURL = base + "/oauth/authorize"
	openaiCodexTokenURL = base + "/oauth/token"
	openaiCodexDeviceUserCodeURL = base + "/api/accounts/deviceauth/usercode"
	openaiCodexDeviceTokenURL = base + "/api/accounts/deviceauth/token"
	openaiCodexDeviceVerifyURI = base + "/codex/device"
	openaiCodexDeviceRedirectURI = base + "/deviceauth/callback"
	return func() {
		openaiCodexAuthorizeURL, openaiCodexTokenURL, openaiCodexDeviceUserCodeURL, openaiCodexDeviceTokenURL, openaiCodexDeviceVerifyURI, openaiCodexDeviceRedirectURI =
			origAuthorize, origToken, origUserCode, origDeviceToken, origVerify, origDeviceRedirect
	}
}

// OpenAICodexToken is Token plus the ChatGPT account id extracted from the
// access token's JWT claims (pi's credentialsFromToken()). AccountID is
// informational only this phase: auth.OAuthCredential has no field for it,
// so it is not persisted -- see login.go's LoginOpenAICodex caller.
type OpenAICodexToken struct {
	Token
	AccountID string
}

const (
	OpenAICodexMethodBrowser    = "browser"
	OpenAICodexMethodDeviceCode = "device_code"
)

// openaiCodexLoginConfig configures LoginOpenAICodex.
type openaiCodexLoginConfig struct {
	port int
}

// OpenAICodexLoginOption configures LoginOpenAICodex.
type OpenAICodexLoginOption func(*openaiCodexLoginConfig)

// WithOpenAICodexCallbackPort overrides the local callback server's port
// (default 1455, matching pi's fixed REDIRECT_URI). Tests use this so the
// callback server does not bind the real port.
func WithOpenAICodexCallbackPort(port int) OpenAICodexLoginOption {
	return func(c *openaiCodexLoginConfig) { c.port = port }
}

func openaiCodexRedirectURI(port int) string {
	return fmt.Sprintf("http://localhost:%d%s", port, openaiCodexCallbackPath)
}

func decodeJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}

// accountIDFromAccessToken extracts chatgpt_account_id from the
// "https://api.openai.com/auth" claim, matching pi's getAccountId().
func accountIDFromAccessToken(access string) string {
	claims := decodeJWTClaims(access)
	if claims == nil {
		return ""
	}
	auth, ok := claims[openaiCodexJWTClaimPath].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := auth["chatgpt_account_id"].(string)
	return id
}

func readOpenAICodexTokenResponse(status int, body map[string]any, raw []byte, operation string) (Token, error) {
	if status < 200 || status >= 300 {
		return Token{}, fmt.Errorf("OpenAI Codex token %s failed (%d): %s", operation, status, string(raw))
	}
	access, ok1 := str(body, "access_token")
	refresh, ok2 := str(body, "refresh_token")
	expiresIn, ok3 := num(body, "expires_in")
	if !ok1 || !ok2 || !ok3 {
		return Token{}, fmt.Errorf("OpenAI Codex token %s response missing fields: %s", operation, string(raw))
	}
	return Token{Access: access, Refresh: refresh, Expires: time.Now().UnixMilli() + int64(expiresIn*1000)}, nil
}

func exchangeOpenAICodexCode(ctx context.Context, code, verifier, redirectURI string) (OpenAICodexToken, error) {
	status, body, raw, err := postForm(ctx, openaiCodexTokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {openaiCodexClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}, nil)
	if err != nil {
		return OpenAICodexToken{}, fmt.Errorf("OpenAI Codex token exchange request failed: %w", err)
	}
	tok, err := readOpenAICodexTokenResponse(status, body, raw, "exchange")
	if err != nil {
		return OpenAICodexToken{}, err
	}
	accountID := accountIDFromAccessToken(tok.Access)
	if accountID == "" {
		return OpenAICodexToken{}, fmt.Errorf("failed to extract accountId from token")
	}
	return OpenAICodexToken{Token: tok, AccountID: accountID}, nil
}

// RefreshOpenAICodexToken exchanges a refresh token for a new access token,
// matching pi's refreshOpenAICodexToken.
func RefreshOpenAICodexToken(ctx context.Context, refreshToken string) (OpenAICodexToken, error) {
	status, body, raw, err := postForm(ctx, openaiCodexTokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {openaiCodexClientID},
	}, nil)
	if err != nil {
		return OpenAICodexToken{}, fmt.Errorf("OpenAI Codex token refresh error: %w", err)
	}
	tok, err := readOpenAICodexTokenResponse(status, body, raw, "refresh")
	if err != nil {
		return OpenAICodexToken{}, err
	}
	accountID := accountIDFromAccessToken(tok.Access)
	if accountID == "" {
		return OpenAICodexToken{}, fmt.Errorf("failed to extract accountId from token")
	}
	return OpenAICodexToken{Token: tok, AccountID: accountID}, nil
}

// --- device code sub-flow ---

type openaiCodexDevice struct {
	deviceAuthID    string
	userCode        string
	intervalSeconds float64
}

func startOpenAICodexDeviceAuth(ctx context.Context) (openaiCodexDevice, error) {
	status, body, raw, err := postJSON(ctx, openaiCodexDeviceUserCodeURL, map[string]any{"client_id": openaiCodexClientID}, nil)
	if err != nil {
		return openaiCodexDevice{}, err
	}
	if status == http.StatusNotFound {
		return openaiCodexDevice{}, fmt.Errorf("OpenAI Codex device code login is not enabled for this server. Use browser login or verify the server URL")
	}
	if status < 200 || status >= 300 {
		return openaiCodexDevice{}, fmt.Errorf("OpenAI Codex device code request failed with status %d: %s", status, string(raw))
	}
	deviceAuthID, ok1 := str(body, "device_auth_id")
	userCode, ok2 := str(body, "user_code")
	interval, ok3 := num(body, "interval")
	if !ok1 || !ok2 || !ok3 || interval < 0 {
		return openaiCodexDevice{}, fmt.Errorf("invalid OpenAI Codex device code response: %s", string(raw))
	}
	return openaiCodexDevice{deviceAuthID: deviceAuthID, userCode: userCode, intervalSeconds: interval}, nil
}

type openaiCodexDeviceCode struct {
	authorizationCode string
	codeVerifier      string
}

func pollOpenAICodexDeviceAuth(ctx context.Context, device openaiCodexDevice) (openaiCodexDeviceCode, error) {
	return PollDeviceCode(ctx, DevicePollOptions{
		IntervalSeconds:  device.intervalSeconds,
		ExpiresInSeconds: openaiCodexDeviceTimeoutS,
	}, func(ctx context.Context) DevicePollResult[openaiCodexDeviceCode] {
		status, body, raw, err := postJSON(ctx, openaiCodexDeviceTokenURL, map[string]any{
			"device_auth_id": device.deviceAuthID,
			"user_code":      device.userCode,
		}, nil)
		if err != nil {
			return DevicePollResult[openaiCodexDeviceCode]{Status: DeviceFailed, Message: err.Error()}
		}
		if status >= 200 && status < 300 {
			authCode, ok1 := str(body, "authorization_code")
			verifier, ok2 := str(body, "code_verifier")
			if !ok1 || !ok2 {
				return DevicePollResult[openaiCodexDeviceCode]{Status: DeviceFailed, Message: fmt.Sprintf("invalid OpenAI Codex device auth token response: %s", string(raw))}
			}
			return DevicePollResult[openaiCodexDeviceCode]{Status: DeviceComplete, Value: openaiCodexDeviceCode{authorizationCode: authCode, codeVerifier: verifier}}
		}
		if status == http.StatusForbidden || status == http.StatusNotFound {
			return DevicePollResult[openaiCodexDeviceCode]{Status: DevicePending}
		}
		var errBody struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(raw, &errBody)
		var errCode string
		if len(errBody.Error) > 0 {
			var s string
			if json.Unmarshal(errBody.Error, &s) == nil {
				errCode = s
			} else {
				var obj struct {
					Code string `json:"code"`
				}
				if json.Unmarshal(errBody.Error, &obj) == nil {
					errCode = obj.Code
				}
			}
		}
		switch errCode {
		case "deviceauth_authorization_pending":
			return DevicePollResult[openaiCodexDeviceCode]{Status: DevicePending}
		case "slow_down":
			return DevicePollResult[openaiCodexDeviceCode]{Status: DeviceSlowDown}
		}
		return DevicePollResult[openaiCodexDeviceCode]{Status: DeviceFailed, Message: fmt.Sprintf("OpenAI Codex device auth failed with status %d: %s", status, string(raw))}
	})
}

func loginOpenAICodexDeviceCode(ctx context.Context, ia FlowInteraction) (OpenAICodexToken, error) {
	device, err := startOpenAICodexDeviceAuth(ctx)
	if err != nil {
		return OpenAICodexToken{}, err
	}
	ia.NotifyDeviceCode(device.userCode, openaiCodexDeviceVerifyURI, int(device.intervalSeconds), openaiCodexDeviceTimeoutS)
	code, err := pollOpenAICodexDeviceAuth(ctx, device)
	if err != nil {
		return OpenAICodexToken{}, err
	}
	return exchangeOpenAICodexCode(ctx, code.authorizationCode, code.codeVerifier, openaiCodexDeviceRedirectURI)
}

// --- browser (PKCE + local callback) sub-flow ---

func startOpenAICodexCallbackServer(state string, port int) (*callbackServer, error) {
	host := anthropicCallbackHost() // shared PI_OAUTH_CALLBACK_HOST lookup
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return nil, err
	}
	cs := &callbackServer{ln: ln, redirectURI: openaiCodexRedirectURI(port), resultCh: make(chan *authCode, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc(openaiCodexCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body>State mismatch.</body></html>")
			return
		}
		code := q.Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body>Missing authorization code.</body></html>")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body>OpenAI authentication completed. You can close this window.</body></html>")
		cs.once.Do(func() { cs.resultCh <- &authCode{code: code, state: state} })
	})
	cs.srv = &http.Server{Handler: mux}
	go func() { _ = cs.srv.Serve(ln) }()
	return cs, nil
}

func loginOpenAICodexBrowser(ctx context.Context, ia FlowInteraction, cfg openaiCodexLoginConfig) (OpenAICodexToken, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return OpenAICodexToken{}, err
	}
	state := pkce.Verifier
	redirectURI := openaiCodexRedirectURI(cfg.port)

	cs, err := startOpenAICodexCallbackServer(state, cfg.port)
	if err != nil {
		return OpenAICodexToken{}, fmt.Errorf("start OAuth callback server: %w", err)
	}
	defer cs.close()

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", openaiCodexClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", openaiCodexScope)
	params.Set("code_challenge", pkce.Challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	params.Set("originator", "harness")

	ia.NotifyAuthURL(openaiCodexAuthorizeURL+"?"+params.Encode(), "A browser window should open. Complete login to finish.")

	manualCtx, cancelManual := context.WithCancel(ctx)
	defer cancelManual()
	type manualResult struct {
		input string
		err   error
	}
	manualCh := make(chan manualResult, 1)
	go func() {
		input, err := ia.PromptManualCode(manualCtx,
			"Complete login in your browser, or paste the authorization code / redirect URL here:", redirectURI)
		manualCh <- manualResult{input, err}
		cs.cancel()
	}()

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
					return OpenAICodexToken{}, fmt.Errorf("state mismatch")
				}
				code = c
				gotState = state
			}
		case <-ctx.Done():
			return OpenAICodexToken{}, ctx.Err()
		}
	}
	if manualErr != nil {
		return OpenAICodexToken{}, manualErr
	}
	if code == "" {
		return OpenAICodexToken{}, fmt.Errorf("missing authorization code")
	}
	_ = gotState

	return exchangeOpenAICodexCode(ctx, code, pkce.Verifier, redirectURI)
}

// LoginOpenAICodex runs OpenAI's Codex (ChatGPT Plus/Pro) OAuth flow: prompts
// for browser-vs-device-code login (matching pi's openaiCodexOAuth.login),
// then runs the chosen sub-flow.
func LoginOpenAICodex(ctx context.Context, ia FlowInteraction, opts ...OpenAICodexLoginOption) (OpenAICodexToken, error) {
	cfg := openaiCodexLoginConfig{port: defaultOpenAICodexPort}
	for _, opt := range opts {
		opt(&cfg)
	}
	method, err := ia.PromptSelect(ctx, "Select OpenAI Codex login method:", []SelectOption{
		{ID: OpenAICodexMethodBrowser, Label: "Browser login (default)"},
		{ID: OpenAICodexMethodDeviceCode, Label: "Device code login (headless)"},
	})
	if err != nil {
		return OpenAICodexToken{}, err
	}
	switch method {
	case OpenAICodexMethodDeviceCode:
		return loginOpenAICodexDeviceCode(ctx, ia)
	case OpenAICodexMethodBrowser, "":
		return loginOpenAICodexBrowser(ctx, ia, cfg)
	default:
		return OpenAICodexToken{}, fmt.Errorf("unknown OpenAI Codex login method: %s", method)
	}
}
