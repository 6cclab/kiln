// Package auth holds the credential store and OAuth flows shared by every
// provider, ported from harness/src/auth/*.ts and pi-ai's
// dist/auth/oauth/*.js.
package auth

import "encoding/json"

// APIKeyCredential is a stored api-key credential. Env holds provider-scoped
// environment/config values such as Cloudflare account/gateway ids.
type APIKeyCredential struct {
	Key string            `json:"key,omitempty"`
	Env map[string]string `json:"env,omitempty"`
}

// OAuthCredential is a stored canonical OAuth credential.
type OAuthCredential struct {
	Refresh string `json:"refresh"`
	Access  string `json:"access"`
	// Expires is epoch milliseconds, already adjusted by the 5-minute
	// refresh margin at store time (see oauth/anthropic.go).
	Expires int64 `json:"expires"`
}

// Credential is one type-tagged credential per provider -- the shape of
// today's credentials.json. Exactly one of APIKey/OAuth is set.
type Credential struct {
	APIKey *APIKeyCredential
	OAuth  *OAuthCredential
}

// credentialType returns the JSON "type" discriminator for c.
func (c Credential) credentialType() string {
	if c.OAuth != nil {
		return "oauth"
	}
	return "api_key"
}

// MarshalJSON flattens Credential into pi's tagged-union shape:
// {"type":"api_key","key":...} or {"type":"oauth","refresh":...}.
func (c Credential) MarshalJSON() ([]byte, error) {
	switch {
	case c.OAuth != nil:
		return json.Marshal(struct {
			Type    string `json:"type"`
			Refresh string `json:"refresh"`
			Access  string `json:"access"`
			Expires int64  `json:"expires"`
		}{"oauth", c.OAuth.Refresh, c.OAuth.Access, c.OAuth.Expires})
	case c.APIKey != nil:
		return json.Marshal(struct {
			Type string            `json:"type"`
			Key  string            `json:"key,omitempty"`
			Env  map[string]string `json:"env,omitempty"`
		}{"api_key", c.APIKey.Key, c.APIKey.Env})
	default:
		return json.Marshal(struct {
			Type string `json:"type"`
		}{"api_key"})
	}
}

// UnmarshalJSON reads pi's tagged-union shape back into a Credential.
func (c *Credential) UnmarshalJSON(data []byte) error {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	switch probe.Type {
	case "oauth":
		var o struct {
			Refresh string `json:"refresh"`
			Access  string `json:"access"`
			Expires int64  `json:"expires"`
		}
		if err := json.Unmarshal(data, &o); err != nil {
			return err
		}
		*c = Credential{OAuth: &OAuthCredential{Refresh: o.Refresh, Access: o.Access, Expires: o.Expires}}
	default:
		var a struct {
			Key string            `json:"key,omitempty"`
			Env map[string]string `json:"env,omitempty"`
		}
		if err := json.Unmarshal(data, &a); err != nil {
			return err
		}
		*c = Credential{APIKey: &APIKeyCredential{Key: a.Key, Env: a.Env}}
	}
	return nil
}

// Info is non-secret credential metadata for account/status enumeration.
type Info struct {
	ProviderID string `json:"providerId"`
	Type       string `json:"type"`
}

// CredentialStore is app-owned credential storage, keyed by provider id, one
// credential per provider. Modify is the only write path, so every mutation
// is a serialized read-modify-write.
type CredentialStore interface {
	// Read returns the stored credential, possibly expired. nil for a
	// missing entry.
	Read(providerID string) (*Credential, error)
	// List returns stored credential metadata without resolving or exposing
	// secrets.
	List() ([]Info, error)
	// Modify is the only write path. fn sees the current credential (nil if
	// absent); a nil return means "leave unchanged", NOT "delete" --
	// deletion is Delete. Returns the post-write credential.
	Modify(providerID string, fn func(current *Credential) (*Credential, error)) error
	// Delete removes a credential (logout).
	Delete(providerID string) error
}
