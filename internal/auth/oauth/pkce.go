// Package oauth implements PKCE and the per-provider OAuth login/refresh
// flows, ported from pi-ai's dist/auth/oauth/*.js.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE is a generated code verifier and its S256 challenge.
type PKCE struct {
	Verifier  string
	Challenge string
}

// base64url matches pi's base64urlEncode: standard base64, then
// URL-safe substitution and stripped padding (RawURLEncoding does exactly
// this).
func base64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// GeneratePKCE generates a PKCE code verifier and its SHA-256 (S256)
// challenge, matching pi-ai's generatePKCE(): a 32-byte random verifier,
// base64url-encoded, challenged with SHA-256 of the verifier's ASCII bytes.
func GeneratePKCE() (PKCE, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return PKCE{}, err
	}
	verifier := base64url(verifierBytes)

	sum := sha256.Sum256([]byte(verifier))
	challenge := base64url(sum[:])

	return PKCE{Verifier: verifier, Challenge: challenge}, nil
}
