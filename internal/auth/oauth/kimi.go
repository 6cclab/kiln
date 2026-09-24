package oauth

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Kimi Code (subscription) OAuth flow, ported from
// pi-ai/dist/auth/oauth/kimi-coding.js: RFC 8628 device authorization
// against auth.kimi.com with JSON (well, form-encoded request / JSON
// response) bodies.

const (
	kimiClientID             = "17e5f671-d194-4dfb-9706-5516cb48c098"
	kimiDefaultOAuthHost     = "https://auth.kimi.com"
	kimiDeviceTimeoutSeconds = 15 * 60
	kimiDefaultPollIntervalS = 5
	kimiRefreshMaxRetries    = 3
)

// kimiOAuthHost honors KIMI_CODE_OAUTH_HOST / KIMI_OAUTH_HOST, matching pi's
// getOauthHost().
func kimiOAuthHost() string {
	host := os.Getenv("KIMI_CODE_OAUTH_HOST")
	if host == "" {
		host = os.Getenv("KIMI_OAUTH_HOST")
	}
	if host == "" {
		host = kimiDefaultOAuthHost
	}
	return strings.TrimRight(host, "/")
}

type kimiDevice struct {
	deviceCode              string
	userCode                string
	verificationURI         string
	verificationURIComplete string
	intervalSeconds         float64
	expiresInSeconds        float64
}

func trustedHTTPURL(value string) bool {
	if value == "" {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http")
}

func startKimiDeviceAuthorization(ctx context.Context, oauthHost string) (kimiDevice, error) {
	status, body, raw, err := postForm(ctx, oauthHost+"/api/oauth/device_authorization", url.Values{
		"client_id": {kimiClientID},
	}, nil)
	if err != nil {
		return kimiDevice{}, err
	}
	if status < 200 || status >= 300 {
		return kimiDevice{}, fmt.Errorf("kimi code device authorization failed with status %d: %s", status, string(raw))
	}
	deviceCode, ok1 := str(body, "device_code")
	userCode, ok2 := str(body, "user_code")
	verificationURI, ok3 := str(body, "verification_uri")
	verificationURIComplete, ok4 := str(body, "verification_uri_complete")
	if !ok1 || !ok2 || !ok3 || !ok4 || !trustedHTTPURL(verificationURI) || !trustedHTTPURL(verificationURIComplete) {
		return kimiDevice{}, fmt.Errorf("invalid Kimi Code device authorization response: %s", string(raw))
	}
	interval, ok := num(body, "interval")
	if !ok || interval <= 0 {
		interval = kimiDefaultPollIntervalS
	}
	expiresIn, ok := num(body, "expires_in")
	if !ok || expiresIn <= 0 {
		expiresIn = kimiDeviceTimeoutSeconds
	}
	return kimiDevice{
		deviceCode:              deviceCode,
		userCode:                userCode,
		verificationURI:         verificationURI,
		verificationURIComplete: verificationURIComplete,
		intervalSeconds:         interval,
		expiresInSeconds:        expiresIn,
	}, nil
}

func parseKimiTokenResponse(body map[string]any, raw []byte, operation string) (Token, error) {
	access, ok1 := str(body, "access_token")
	refresh, ok2 := str(body, "refresh_token")
	expiresIn, ok3 := num(body, "expires_in")
	if !ok1 || !ok2 || !ok3 || expiresIn <= 0 {
		return Token{}, fmt.Errorf("kimi code token %s response missing fields: %s", operation, string(raw))
	}
	return Token{Access: access, Refresh: refresh, Expires: time.Now().UnixMilli() + int64(expiresIn*1000)}, nil
}

func pollForKimiToken(ctx context.Context, oauthHost string, device kimiDevice) (Token, error) {
	return PollDeviceCode(ctx, DevicePollOptions{
		IntervalSeconds:     device.intervalSeconds,
		ExpiresInSeconds:    device.expiresInSeconds,
		WaitBeforeFirstPoll: true,
	}, func(ctx context.Context) DevicePollResult[Token] {
		status, body, raw, err := postForm(ctx, oauthHost+"/api/oauth/token", url.Values{
			"client_id":   {kimiClientID},
			"device_code": {device.deviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}, nil)
		if err != nil {
			return DevicePollResult[Token]{Status: DeviceFailed, Message: err.Error()}
		}
		if status >= 500 {
			return DevicePollResult[Token]{Status: DeviceFailed, Message: fmt.Sprintf("kimi code device token request failed with status %d: %s", status, string(raw))}
		}
		if status >= 200 && status < 300 {
			if _, ok := str(body, "access_token"); ok {
				tok, err := parseKimiTokenResponse(body, raw, "poll")
				if err != nil {
					return DevicePollResult[Token]{Status: DeviceFailed, Message: err.Error()}
				}
				return DevicePollResult[Token]{Status: DeviceComplete, Value: tok}
			}
		}
		errCode, _ := str(body, "error")
		desc, _ := str(body, "error_description")
		descSuffix := ""
		if desc != "" {
			descSuffix = ": " + desc
		}
		switch errCode {
		case "authorization_pending":
			return DevicePollResult[Token]{Status: DevicePending}
		case "slow_down":
			var iv *float64
			if v, ok := num(body, "interval"); ok && v > 0 {
				iv = &v
			}
			return DevicePollResult[Token]{Status: DeviceSlowDown, IntervalSeconds: iv}
		case "expired_token":
			return DevicePollResult[Token]{Status: DeviceFailed, Message: "kimi code device authorization expired. Please restart login."}
		case "access_denied":
			return DevicePollResult[Token]{Status: DeviceFailed, Message: "kimi code login was denied."}
		}
		return DevicePollResult[Token]{Status: DeviceFailed, Message: fmt.Sprintf("kimi code device token request failed (status %d)%s%s", status, errCode, descSuffix)}
	})
}

func kimiRetryableRefreshFailure(status int) bool {
	return status == 429 || status >= 500
}

// RefreshKimiToken refreshes a Kimi Code access token, retrying transient
// (429/5xx) failures up to 3 times with exponential backoff, matching pi's
// refreshToken(). A 401/403/invalid_grant response is not retried: the
// credential is dead and the caller should prompt re-login.
func RefreshKimiToken(ctx context.Context, refreshToken string) (Token, error) {
	oauthHost := kimiOAuthHost()
	var lastErr error
	for attempt := 0; attempt <= kimiRefreshMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1000*(1<<(attempt-1))) * time.Millisecond
			if err := abortableSleep(ctx, backoff); err != nil {
				return Token{}, fmt.Errorf("kimi code token refresh aborted")
			}
		}
		if ctx.Err() != nil {
			return Token{}, fmt.Errorf("kimi code token refresh aborted")
		}
		status, body, raw, err := postForm(ctx, oauthHost+"/api/oauth/token", url.Values{
			"client_id":     {kimiClientID},
			"grant_type":    {"refresh_token"},
			"refresh_token": {refreshToken},
		}, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if status >= 200 && status < 300 {
			return parseKimiTokenResponse(body, raw, "refresh")
		}
		errCode, _ := str(body, "error")
		if status == 401 || status == 403 || errCode == "invalid_grant" {
			desc, _ := str(body, "error_description")
			suffix := ""
			if desc != "" {
				suffix = ": " + desc
			}
			return Token{}, fmt.Errorf("kimi code token refresh unauthorized (status %d)%s", status, suffix)
		}
		if kimiRetryableRefreshFailure(status) && attempt < kimiRefreshMaxRetries {
			lastErr = fmt.Errorf("kimi code token refresh failed with status %d", status)
			continue
		}
		return Token{}, fmt.Errorf("kimi code token refresh failed with status %d: %s", status, string(raw))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("kimi code token refresh failed")
	}
	return Token{}, lastErr
}

// LoginKimi runs Kimi Code's device-authorization login flow.
func LoginKimi(ctx context.Context, ia FlowInteraction) (Token, error) {
	oauthHost := kimiOAuthHost()
	device, err := startKimiDeviceAuthorization(ctx, oauthHost)
	if err != nil {
		return Token{}, err
	}
	ia.NotifyDeviceCode(device.userCode, device.verificationURIComplete, int(device.intervalSeconds), int(device.expiresInSeconds))
	return pollForKimiToken(ctx, oauthHost, device)
}
