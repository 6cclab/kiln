package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestGeneratePKCE(t *testing.T) {
	p, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if p.Verifier == "" || p.Challenge == "" {
		t.Fatal("expected non-empty verifier and challenge")
	}
	if strings.ContainsAny(p.Verifier, "+/=") || strings.ContainsAny(p.Challenge, "+/=") {
		t.Fatalf("expected base64url (no +/=), got verifier=%q challenge=%q", p.Verifier, p.Challenge)
	}

	sum := sha256.Sum256([]byte(p.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if p.Challenge != want {
		t.Fatalf("challenge = %q, want S256(verifier) = %q", p.Challenge, want)
	}

	p2, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if p.Verifier == p2.Verifier {
		t.Fatal("two calls produced the same verifier; RNG likely broken")
	}
}
