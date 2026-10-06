package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/crash"
)

// Radius gateway OAuth flow, ported from
// pi-ai/dist/auth/oauth/radius.js. Radius is a pi-messages gateway; only the
// interactive browser authorization endpoint is discovered (GET
// /v1/oauth), the token endpoint is fixed (/v1/oauth/token), and the
// device-code endpoint is fixed (/v1/oauth/device). Model catalog loading
// (GET /v1/config) is the Radius provider's concern (builtin), not oauth's.

const (
	RadiusDefaultGateway    = "https://radius.pi.dev"
	radiusCallbackHost      = "127.0.0.1"
	radiusCallbackPort      = 1456
	radiusCallbackPath      = "/oauth/callback"
	radiusTokenExpirySkewMs = 60_000
	radiusClientID          = "pi-gateway"
	radiusScope             = "gateway offline_access"
	radiusDeviceGrantType   = "urn:ietf:params:oauth:grant-type:device_code"

	RadiusLoginMethodBrowser    = "browser"
	RadiusLoginMethodDeviceCode = "device-code"
)

var radiusPortOverride = radiusCallbackPort

// SetRadiusCallbackPortForTesting overrides the local callback server's
// port (default 1456, matching pi's fixed REDIRECT_URI) for the duration of
// a test.
func SetRadiusCallbackPortForTesting(port int) (restore func()) {
	orig := radiusPortOverride
	radiusPortOverride = port
	return func() { radiusPortOverride = orig }
}

var trailingSlashes = regexp.MustCompile(`/+$`)
var schemePrefix = regexp.MustCompile(`(?i)^https?://`)

// NormalizeRadiusGatewayURL matches pi's normalizeRadiusGatewayUrl: adds an
// https:// scheme when none is present, and strips trailing slashes.
func NormalizeRadiusGatewayURL(value string) string {
	withScheme := value
	if !schemePrefix.MatchString(value) {
		withScheme = "https://" + value
	}
	return trailingSlashes.ReplaceAllString(withScheme, "")
}

func radiusRedirectURI() string {
	return fmt.Sprintf("http://%s:%d%s", radiusCallbackHost, radiusPortOverride, radiusCallbackPath)
}

// RadiusOAuthResponseError carries the OAuth `error` code (e.g.
// "authorization_pending", "slow_down") so device-code polling can branch
// on it, matching pi's OAuthResponseError.
type RadiusOAuthResponseError struct {
	Status      int
	OAuthError  string
	Description string
}

func (e *RadiusOAuthResponseError) Error() string {
	detail := e.OAuthError
	if e.Description != "" {
		if detail != "" {
			detail += ": " + e.Description
		} else {
			detail = e.Description
		}
	}
	if detail == "" {
		detail = fmt.Sprintf("%d", e.Status)
	}
	return "Radius OAuth request failed: " + detail
}

func radiusReadError(status int, body map[string]any, raw []byte) error {
	errCode, _ := str(body, "error")
	desc, _ := str(body, "error_description")
	if errCode == "" && desc == "" && len(raw) > 0 {
		desc = string(raw)
	}
	return &RadiusOAuthResponseError{Status: status, OAuthError: errCode, Description: desc}
}

// RadiusDiscovery is the subset of GET {gateway}/v1/oauth this package
// reads.
type RadiusDiscovery struct {
	AuthorizationEndpoint string
}

// LoadRadiusOAuthDiscovery fetches the gateway's OAuth discovery document.
func LoadRadiusOAuthDiscovery(ctx context.Context, gateway string) (RadiusDiscovery, error) {
	status, body, raw, err := getJSON(ctx, gateway+"/v1/oauth", nil)
	if err != nil {
		return RadiusDiscovery{}, err
	}
	if status < 200 || status >= 300 {
		return RadiusDiscovery{}, fmt.Errorf("could not load Radius OAuth config from %s: %d: %s", gateway, status, string(raw))
	}
	endpoint, ok := str(body, "authorizationEndpoint")
	if !ok {
		return RadiusDiscovery{}, fmt.Errorf("invalid Radius OAuth config from %s", gateway)
	}
	return RadiusDiscovery{AuthorizationEndpoint: endpoint}, nil
}

func requestRadiusOAuthToken(ctx context.Context, gateway string, fields url.Values) (Token, error) {
	status, body, raw, err := postForm(ctx, gateway+"/v1/oauth/token", fields, nil)
	if err != nil {
		return Token{}, err
	}
	if status < 200 || status >= 300 {
		return Token{}, radiusReadError(status, body, raw)
	}
	access, ok1 := str(body, "access_token")
	refresh, ok2 := str(body, "refresh_token")
	expiresIn, ok3 := num(body, "expires_in")
	if !ok1 || !ok2 || !ok3 {
		return Token{}, fmt.Errorf("invalid Radius OAuth token response: %s", string(raw))
	}
	return Token{Access: access, Refresh: refresh, Expires: time.Now().UnixMilli() + int64(expiresIn*1000) - radiusTokenExpirySkewMs}, nil
}

// --- browser sub-flow ---

type radiusCallbackServer struct {
	srv      *http.Server
	ln       net.Listener
	resultCh chan *string
	once     sync.Once
}

func startRadiusCallbackServer(expectedState string) (*radiusCallbackServer, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", radiusCallbackHost, radiusPortOverride))
	if err != nil {
		return nil, err
	}
	cs := &radiusCallbackServer{ln: ln, resultCh: make(chan *string, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc(radiusCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != expectedState {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body>OAuth state mismatch.</body></html>")
			return
		}
		if e := q.Get("error"); e != "" {
			desc := q.Get("error_description")
			if desc == "" {
				desc = e
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body>%s</body></html>", desc)
			cs.once.Do(func() { cs.resultCh <- nil })
			return
		}
		code := q.Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body>Missing authorization code.</body></html>")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body>Signed in to Radius. You may now close this page.</body></html>")
		cs.once.Do(func() { cs.resultCh <- &code })
	})
	cs.srv = &http.Server{Handler: mux}
	crash.Go(func() { _ = cs.srv.Serve(ln) })
	return cs, nil
}

func (cs *radiusCallbackServer) waitForCode(ctx context.Context) *string {
	select {
	case r := <-cs.resultCh:
		return r
	case <-ctx.Done():
		cs.once.Do(func() { cs.resultCh <- nil })
		return nil
	}
}

func (cs *radiusCallbackServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = cs.srv.Shutdown(ctx)
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func loginRadiusWithBrowser(ctx context.Context, gateway, authorizationEndpoint string, ia FlowInteraction) (Token, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return Token{}, err
	}
	state, err := randomState()
	if err != nil {
		return Token{}, err
	}
	redirectURI := radiusRedirectURI()

	authorizeURL, err := url.Parse(authorizationEndpoint)
	if err != nil {
		return Token{}, fmt.Errorf("invalid Radius authorization endpoint: %w", err)
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", radiusClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", radiusScope)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("handoff", "url")
	q.Set("state", state)
	authorizeURL.RawQuery = q.Encode()

	cs, err := startRadiusCallbackServer(state)
	if err != nil {
		return Token{}, fmt.Errorf("start OAuth callback server: %w", err)
	}
	defer cs.close()

	ia.NotifyProgress(fmt.Sprintf("Listening for OAuth callback on %s", redirectURI))
	ia.NotifyAuthURL(authorizeURL.String(), "Continue in your browser.")

	code := cs.waitForCode(ctx)
	if code == nil {
		if ctx.Err() != nil {
			return Token{}, ErrDeviceCancelled
		}
		return Token{}, fmt.Errorf("OAuth callback did not complete")
	}
	return requestRadiusOAuthToken(ctx, gateway, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {radiusClientID},
		"redirect_uri":  {redirectURI},
		"code":          {*code},
		"code_verifier": {pkce.Verifier},
	})
}

// --- device-code sub-flow ---

type radiusDevice struct {
	deviceCode      string
	userCode        string
	verificationURI string
	intervalSeconds float64
	expiresIn       float64
}

func requestRadiusDeviceAuthorization(ctx context.Context, gateway string) (radiusDevice, error) {
	status, body, raw, err := postForm(ctx, gateway+"/v1/oauth/device", url.Values{
		"client_id": {radiusClientID},
		"scope":     {radiusScope},
	}, nil)
	if err != nil {
		return radiusDevice{}, err
	}
	if status < 200 || status >= 300 {
		return radiusDevice{}, radiusReadError(status, body, raw)
	}
	deviceCode, ok1 := str(body, "device_code")
	userCode, ok2 := str(body, "user_code")
	verificationURI, ok3 := str(body, "verification_uri")
	expiresIn, ok4 := num(body, "expires_in")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return radiusDevice{}, fmt.Errorf("radius OAuth device authorization response is missing required fields")
	}
	interval, _ := num(body, "interval")
	return radiusDevice{deviceCode: deviceCode, userCode: userCode, verificationURI: verificationURI, intervalSeconds: interval, expiresIn: expiresIn}, nil
}

func loginRadiusWithDeviceCode(ctx context.Context, gateway string, ia FlowInteraction) (Token, error) {
	device, err := requestRadiusDeviceAuthorization(ctx, gateway)
	if err != nil {
		return Token{}, err
	}
	ia.NotifyDeviceCode(device.userCode, device.verificationURI, int(device.intervalSeconds), int(device.expiresIn))
	return PollDeviceCode(ctx, DevicePollOptions{
		IntervalSeconds:  device.intervalSeconds,
		ExpiresInSeconds: device.expiresIn,
	}, func(ctx context.Context) DevicePollResult[Token] {
		tok, err := requestRadiusOAuthToken(ctx, gateway, url.Values{
			"grant_type":  {radiusDeviceGrantType},
			"client_id":   {radiusClientID},
			"device_code": {device.deviceCode},
		})
		if err == nil {
			return DevicePollResult[Token]{Status: DeviceComplete, Value: tok}
		}
		var oerr *RadiusOAuthResponseError
		if e, ok := err.(*RadiusOAuthResponseError); ok {
			oerr = e
		}
		if oerr == nil {
			return DevicePollResult[Token]{Status: DeviceFailed, Message: err.Error()}
		}
		switch oerr.OAuthError {
		case "authorization_pending":
			return DevicePollResult[Token]{Status: DevicePending}
		case "slow_down":
			return DevicePollResult[Token]{Status: DeviceSlowDown}
		case "expired_token":
			return DevicePollResult[Token]{Status: DeviceFailed, Message: "Device authorization expired."}
		case "access_denied":
			return DevicePollResult[Token]{Status: DeviceFailed, Message: "Device authorization was denied."}
		default:
			return DevicePollResult[Token]{Status: DeviceFailed, Message: err.Error()}
		}
	})
}

// LoginRadius runs Radius's sign-in flow: prompts for browser-vs-device-code
// (matching pi's createRadiusOAuth().login), then runs the chosen sub-flow
// against gateway (already normalized).
func LoginRadius(ctx context.Context, ia FlowInteraction, name, gateway string) (Token, error) {
	gateway = NormalizeRadiusGatewayURL(gateway)
	method, err := ia.PromptSelect(ctx, fmt.Sprintf("Sign in to %s:", name), []SelectOption{
		{ID: RadiusLoginMethodBrowser, Label: "Sign in with browser (recommended)"},
		{ID: RadiusLoginMethodDeviceCode, Label: "Sign in with device code (when signing in from another device)"},
	})
	if err != nil {
		return Token{}, err
	}
	switch method {
	case RadiusLoginMethodDeviceCode:
		return loginRadiusWithDeviceCode(ctx, gateway, ia)
	case RadiusLoginMethodBrowser, "":
		discovery, err := LoadRadiusOAuthDiscovery(ctx, gateway)
		if err != nil {
			return Token{}, err
		}
		return loginRadiusWithBrowser(ctx, gateway, discovery.AuthorizationEndpoint, ia)
	default:
		return Token{}, fmt.Errorf("unknown %s sign-in method: %s", name, method)
	}
}

// RefreshRadiusToken refreshes a Radius gateway token.
func RefreshRadiusToken(ctx context.Context, gateway, refreshToken string) (Token, error) {
	gateway = NormalizeRadiusGatewayURL(gateway)
	return requestRadiusOAuthToken(ctx, gateway, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {radiusClientID},
		"refresh_token": {refreshToken},
	})
}
