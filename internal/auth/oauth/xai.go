package oauth

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// xAI OAuth device-code flow, ported from pi-ai/dist/auth/oauth/xai.js.

const (
	xaiClientID         = "b1a00492-073a-47ea-816f-4c329264a828"
	xaiScope            = "openid profile email offline_access grok-cli:access api:access"
	xaiRefreshSkewMs    = 5 * 60 * 1000
	xaiDefaultLifetimeS = 3600
)

var (
	xaiDeviceCodeURL = "https://auth.x.ai/oauth2/device/code"
	xaiTokenURL      = "https://auth.x.ai/oauth2/token"
)

// SetXaiURLsForTesting points xAI's device-code/token endpoints at an
// httptest server for the duration of a test.
func SetXaiURLsForTesting(deviceCodeURL, tokenURL string) (restore func()) {
	origDevice, origToken := xaiDeviceCodeURL, xaiTokenURL
	xaiDeviceCodeURL = deviceCodeURL
	xaiTokenURL = tokenURL
	return func() { xaiDeviceCodeURL, xaiTokenURL = origDevice, origToken }
}

type xaiDevice struct {
	deviceCode              string
	userCode                string
	verificationURI         string
	verificationURIComplete string
	intervalSeconds         float64
	expiresInSeconds        float64
}

func xaiValidateVerificationURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("untrusted verification URI in xAI OAuth response")
	}
	return u.String(), nil
}

func requestXaiDeviceCode(ctx context.Context) (xaiDevice, error) {
	status, body, raw, err := postForm(ctx, xaiDeviceCodeURL, url.Values{
		"client_id": {xaiClientID},
		"scope":     {xaiScope},
		"referrer":  {"harness"},
	}, nil)
	if err != nil {
		return xaiDevice{}, fmt.Errorf("login cancelled: %w", err)
	}
	if status < 200 || status >= 300 {
		return xaiDevice{}, xaiRequestFailure("device authorization", status, body)
	}
	deviceCode, ok1 := str(body, "device_code")
	userCode, ok2 := str(body, "user_code")
	verificationURI, ok3 := str(body, "verification_uri")
	expiresIn, ok4 := num(body, "expires_in")
	if !ok1 || !ok2 || !ok3 || !ok4 || expiresIn <= 0 {
		return xaiDevice{}, fmt.Errorf("invalid xAI OAuth response: %s", string(raw))
	}
	verified, err := xaiValidateVerificationURI(verificationURI)
	if err != nil {
		return xaiDevice{}, err
	}
	var verifiedComplete string
	if vc, ok := str(body, "verification_uri_complete"); ok {
		verifiedComplete, err = xaiValidateVerificationURI(vc)
		if err != nil {
			return xaiDevice{}, err
		}
	}
	interval, _ := num(body, "interval")
	return xaiDevice{
		deviceCode:              deviceCode,
		userCode:                userCode,
		verificationURI:         verified,
		verificationURIComplete: verifiedComplete,
		intervalSeconds:         interval,
		expiresInSeconds:        expiresIn,
	}, nil
}

func xaiRequestFailure(action string, status int, body map[string]any) error {
	errCode, _ := str(body, "error")
	desc, _ := str(body, "error_description")
	detail := errCode
	if desc != "" {
		if detail != "" {
			detail += ": " + desc
		} else {
			detail = desc
		}
	}
	if detail != "" {
		return fmt.Errorf("xAI OAuth %s failed (HTTP %d): %s", action, status, detail)
	}
	return fmt.Errorf("xAI OAuth %s failed (HTTP %d)", action, status)
}

func xaiCredentialsFromTokenResponse(body map[string]any, previousRefresh string) (Token, error) {
	access, ok := str(body, "access_token")
	if !ok {
		return Token{}, fmt.Errorf("invalid xAI OAuth response field: access_token")
	}
	refresh, ok := str(body, "refresh_token")
	if !ok {
		if previousRefresh == "" {
			return Token{}, fmt.Errorf("invalid xAI OAuth response field: refresh_token")
		}
		refresh = previousRefresh
	}
	expiresIn, ok := num(body, "expires_in")
	if !ok {
		expiresIn = xaiDefaultLifetimeS
	} else if expiresIn <= 0 {
		return Token{}, fmt.Errorf("invalid xAI OAuth response field: expires_in")
	}
	return Token{
		Access:  access,
		Refresh: refresh,
		Expires: time.Now().UnixMilli() + int64(expiresIn*1000) - xaiRefreshSkewMs,
	}, nil
}

func pollForXaiTokens(ctx context.Context, device xaiDevice) (Token, error) {
	return PollDeviceCode(ctx, DevicePollOptions{
		IntervalSeconds:     device.intervalSeconds,
		ExpiresInSeconds:    device.expiresInSeconds,
		WaitBeforeFirstPoll: true,
	}, func(ctx context.Context) DevicePollResult[Token] {
		status, body, _, err := postForm(ctx, xaiTokenURL, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {xaiClientID},
			"device_code": {device.deviceCode},
		}, nil)
		if err != nil {
			return DevicePollResult[Token]{Status: DeviceFailed, Message: err.Error()}
		}
		if status >= 200 && status < 300 {
			tok, err := xaiCredentialsFromTokenResponse(body, "")
			if err != nil {
				return DevicePollResult[Token]{Status: DeviceFailed, Message: err.Error()}
			}
			return DevicePollResult[Token]{Status: DeviceComplete, Value: tok}
		}
		errCode, _ := str(body, "error")
		switch errCode {
		case "authorization_pending":
			return DevicePollResult[Token]{Status: DevicePending}
		case "slow_down":
			var iv *float64
			if v, ok := num(body, "interval"); ok {
				iv = &v
			}
			return DevicePollResult[Token]{Status: DeviceSlowDown, IntervalSeconds: iv}
		case "access_denied", "authorization_denied":
			return DevicePollResult[Token]{Status: DeviceFailed, Message: "xAI device authorization was denied"}
		case "expired_token":
			return DevicePollResult[Token]{Status: DeviceFailed, Message: "xAI device code expired"}
		}
		return DevicePollResult[Token]{Status: DeviceFailed, Message: xaiRequestFailure("device token polling", status, body).Error()}
	})
}

// LoginXai runs xAI's device-code flow.
func LoginXai(ctx context.Context, ia FlowInteraction) (Token, error) {
	device, err := requestXaiDeviceCode(ctx)
	if err != nil {
		return Token{}, err
	}
	verificationURI := device.verificationURIComplete
	if verificationURI == "" {
		verificationURI = device.verificationURI
	}
	ia.NotifyDeviceCode(device.userCode, verificationURI, int(device.intervalSeconds), int(device.expiresInSeconds))
	return pollForXaiTokens(ctx, device)
}

// RefreshXaiToken refreshes an xAI access token. xAI may omit refresh_token
// on refresh when the token is not rotated; the previous refresh token is
// carried forward in that case, matching pi's credentialsFromTokenResponse.
func RefreshXaiToken(ctx context.Context, refreshToken string) (Token, error) {
	status, body, _, err := postForm(ctx, xaiTokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {xaiClientID},
		"refresh_token": {refreshToken},
	}, nil)
	if err != nil {
		return Token{}, err
	}
	if status < 200 || status >= 300 {
		return Token{}, xaiRequestFailure("token refresh", status, body)
	}
	return xaiCredentialsFromTokenResponse(body, refreshToken)
}
