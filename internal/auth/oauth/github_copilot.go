package oauth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// GitHub Copilot OAuth flow, ported from
// pi-ai/dist/auth/oauth/github-copilot.js.
//
// Two-stage credential: a GitHub device-code login yields a long-lived
// GitHub access token (never expires on its own; only revocation kills it).
// That token is exchanged at copilot_internal/v2/token for a short-lived
// Copilot session token which must be refreshed regularly. This package
// models the two stages the same way pi does: Token.Refresh holds the
// GitHub access token (the thing device-code login actually produces),
// Token.Access holds the current Copilot session token, and Token.Expires
// is the *session* token's expiry -- so builtin's resolveAuth's "refresh
// when past Expires" logic transparently re-derives a new Copilot session
// token from the still-valid GitHub token, exactly like pi's refresh().
const (
	githubCopilotClientIDB64 = "SXYxLmI1MDdhMDhjODdlY2ZlOTg=" // decodes to "Iv1.b507a08c87ecfe98"
	githubCopilotAPIVersion  = "2026-06-01"
)

var githubCopilotHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

// GitHubCopilotHeaders returns a copy of the static headers pi sends on
// every GitHub Copilot request (device flow, token refresh, and -- for the
// api client package this phase does not own -- chat completions).
func GitHubCopilotHeaders() map[string]string {
	out := make(map[string]string, len(githubCopilotHeaders))
	for k, v := range githubCopilotHeaders {
		out[k] = v
	}
	return out
}

func githubCopilotClientID() string {
	// atob() of the constant above, matching pi's `decode = (s) => atob(s)`.
	decoded, err := base64.StdEncoding.DecodeString(githubCopilotClientIDB64)
	if err != nil {
		panic("oauth: invalid embedded GitHub Copilot client id: " + err.Error())
	}
	return string(decoded)
}

type githubCopilotURLs struct {
	deviceCodeURL   string
	accessTokenURL  string
	copilotTokenURL string
}

// githubCopilotScheme is a var, not a hardcoded "https", so tests can point
// it at a plain-http httptest server. api.%s (the Copilot token host) is
// dropped in that case too: tests substitute a full host:port for domain
// and stand up one httptest server for all three routes.
var githubCopilotScheme = "https"
var githubCopilotAPIHostPrefix = "api."

// SetGitHubCopilotSchemeForTesting points every GitHub Copilot OAuth
// endpoint at scheme (e.g. "http") and drops the "api." host prefix so a
// single httptest server (domain == its host:port) serves all three
// routes, for the duration of a test.
func SetGitHubCopilotSchemeForTesting(scheme string) (restore func()) {
	origScheme, origPrefix := githubCopilotScheme, githubCopilotAPIHostPrefix
	githubCopilotScheme = scheme
	githubCopilotAPIHostPrefix = ""
	return func() { githubCopilotScheme, githubCopilotAPIHostPrefix = origScheme, origPrefix }
}

func githubCopilotURLsFor(domain string) githubCopilotURLs {
	return githubCopilotURLs{
		deviceCodeURL:   fmt.Sprintf("%s://%s/login/device/code", githubCopilotScheme, domain),
		accessTokenURL:  fmt.Sprintf("%s://%s/login/oauth/access_token", githubCopilotScheme, domain),
		copilotTokenURL: fmt.Sprintf("%s://%s%s/copilot_internal/v2/token", githubCopilotScheme, githubCopilotAPIHostPrefix, domain),
	}
}

var proxyEpPattern = regexp.MustCompile(`proxy-ep=([^;]+)`)

// githubCopilotBaseURLFromToken extracts the API base URL from a Copilot
// session token's proxy-ep claim (a semicolon-delimited string, not a JWT),
// matching pi's getBaseUrlFromToken/getGitHubCopilotBaseUrl.
func githubCopilotBaseURLFromToken(token string) string {
	m := proxyEpPattern.FindStringSubmatch(token)
	if m == nil {
		return ""
	}
	apiHost := strings.Replace(m[1], "proxy.", "api.", 1)
	return "https://" + apiHost
}

// GitHubCopilotBaseURL returns the Copilot API base URL for token, falling
// back to the enterprise or individual default, matching pi's
// getGitHubCopilotBaseUrl.
func GitHubCopilotBaseURL(token, enterpriseDomain string) string {
	if token != "" {
		if u := githubCopilotBaseURLFromToken(token); u != "" {
			return u
		}
	}
	if enterpriseDomain != "" {
		return "https://copilot-api." + enterpriseDomain
	}
	return "https://api.individual.githubcopilot.com"
}

// NormalizeGitHubDomain matches pi's normalizeDomain(): accepts a bare host
// or a full URL, returns the hostname, or "" if input is blank or
// unparsable.
func NormalizeGitHubDomain(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", nil
	}
	raw := trimmed
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid GitHub Enterprise URL/domain")
	}
	return u.Hostname(), nil
}

type githubDeviceAuth struct {
	deviceCode      string
	userCode        string
	verificationURI string
	intervalSeconds float64
	expiresInSecs   float64
}

func startGitHubDeviceFlow(ctx context.Context, domain string) (githubDeviceAuth, error) {
	urls := githubCopilotURLsFor(domain)
	status, body, raw, err := postForm(ctx, urls.deviceCodeURL, url.Values{
		"client_id": {githubCopilotClientID()},
		"scope":     {"read:user"},
	}, map[string]string{"User-Agent": "GitHubCopilotChat/0.35.0"})
	if err != nil {
		return githubDeviceAuth{}, err
	}
	if status < 200 || status >= 300 {
		return githubDeviceAuth{}, fmt.Errorf("%d: %s", status, string(raw))
	}
	deviceCode, ok1 := str(body, "device_code")
	userCode, ok2 := str(body, "user_code")
	verificationURI, ok3 := str(body, "verification_uri")
	expiresIn, ok4 := num(body, "expires_in")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return githubDeviceAuth{}, fmt.Errorf("invalid device code response fields")
	}
	parsed, err := url.Parse(verificationURI)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return githubDeviceAuth{}, fmt.Errorf("untrusted verification_uri in device code response")
	}
	interval, _ := num(body, "interval")
	return githubDeviceAuth{
		deviceCode:      deviceCode,
		userCode:        userCode,
		verificationURI: parsed.String(),
		intervalSeconds: interval,
		expiresInSecs:   expiresIn,
	}, nil
}

func pollForGitHubAccessToken(ctx context.Context, domain string, device githubDeviceAuth) (string, error) {
	urls := githubCopilotURLsFor(domain)
	return PollDeviceCode(ctx, DevicePollOptions{
		IntervalSeconds:     device.intervalSeconds,
		ExpiresInSeconds:    device.expiresInSecs,
		WaitBeforeFirstPoll: true,
	}, func(ctx context.Context) DevicePollResult[string] {
		status, body, raw, err := postForm(ctx, urls.accessTokenURL, url.Values{
			"client_id":   {githubCopilotClientID()},
			"device_code": {device.deviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}, map[string]string{"User-Agent": "GitHubCopilotChat/0.35.0"})
		if err != nil {
			return DevicePollResult[string]{Status: DeviceFailed, Message: err.Error()}
		}
		if status < 200 || status >= 300 {
			return DevicePollResult[string]{Status: DeviceFailed, Message: fmt.Sprintf("device flow failed: %d: %s", status, string(raw))}
		}
		if token, ok := str(body, "access_token"); ok {
			return DevicePollResult[string]{Status: DeviceComplete, Value: token}
		}
		errCode, _ := str(body, "error")
		switch errCode {
		case "authorization_pending":
			return DevicePollResult[string]{Status: DevicePending}
		case "slow_down":
			var iv *float64
			if v, ok := num(body, "interval"); ok {
				iv = &v
			}
			return DevicePollResult[string]{Status: DeviceSlowDown, IntervalSeconds: iv}
		}
		desc, _ := str(body, "error_description")
		suffix := ""
		if desc != "" {
			suffix = ": " + desc
		}
		return DevicePollResult[string]{Status: DeviceFailed, Message: fmt.Sprintf("device flow failed: %s%s", errCode, suffix)}
	})
}

// GitHubCopilotCredential is the two-stage credential state: refresh is the
// permanent GitHub access token, access/expires are the current derived
// Copilot session token and its expiry.
type GitHubCopilotCredential struct {
	Token
	EnterpriseDomain string
}

func refreshGitHubCopilotAccessToken(ctx context.Context, githubAccessToken, enterpriseDomain string) (GitHubCopilotCredential, error) {
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	urls := githubCopilotURLsFor(domain)
	headers := GitHubCopilotHeaders()
	headers["Authorization"] = "Bearer " + githubAccessToken
	headers["Accept"] = "application/json"
	status, body, raw, err := getJSON(ctx, urls.copilotTokenURL, headers)
	if err != nil {
		return GitHubCopilotCredential{}, err
	}
	if status < 200 || status >= 300 {
		return GitHubCopilotCredential{}, fmt.Errorf("%d: %s", status, string(raw))
	}
	token, ok1 := str(body, "token")
	expiresAt, ok2 := num(body, "expires_at")
	if !ok1 || !ok2 {
		return GitHubCopilotCredential{}, fmt.Errorf("invalid Copilot token response fields: %s", string(raw))
	}
	return GitHubCopilotCredential{
		Token:            Token{Refresh: githubAccessToken, Access: token, Expires: int64(expiresAt)*1000 - fiveMinuteMarginMs},
		EnterpriseDomain: enterpriseDomain,
	}, nil
}

// RefreshGitHubCopilotToken re-derives a Copilot session token from the
// still-valid GitHub access token, matching pi's refreshGitHubCopilotToken
// (minus the model-catalog fetch, which is builtin's concern, not oauth's).
func RefreshGitHubCopilotToken(ctx context.Context, githubAccessToken, enterpriseDomain string) (GitHubCopilotCredential, error) {
	return refreshGitHubCopilotAccessToken(ctx, githubAccessToken, enterpriseDomain)
}

// LoginGitHubCopilot runs the device-code + Copilot-token-exchange flow.
// domainInput is the GitHub Enterprise URL/domain the caller collected
// (blank for github.com), matching pi's loginGitHubCopilot's own prompt for
// it -- callers of this package should still ask, but the prompt itself is
// left to login.go since it is a plain text question with no OAuth
// semantics.
func LoginGitHubCopilot(ctx context.Context, ia FlowInteraction, domainInput string) (GitHubCopilotCredential, error) {
	enterpriseDomain, err := NormalizeGitHubDomain(domainInput)
	if err != nil {
		return GitHubCopilotCredential{}, err
	}
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	device, err := startGitHubDeviceFlow(ctx, domain)
	if err != nil {
		return GitHubCopilotCredential{}, err
	}
	ia.NotifyDeviceCode(device.userCode, device.verificationURI, int(device.intervalSeconds), int(device.expiresInSecs))
	githubAccessToken, err := pollForGitHubAccessToken(ctx, domain, device)
	if err != nil {
		return GitHubCopilotCredential{}, err
	}
	return refreshGitHubCopilotAccessToken(ctx, githubAccessToken, enterpriseDomain)
}
