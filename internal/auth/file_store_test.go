package auth

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFileCredentialStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	s := NewFileCredentialStore(path)

	// Absent file = empty.
	c, err := s.Read("anthropic")
	if err != nil {
		t.Fatalf("Read on absent file: %v", err)
	}
	if c != nil {
		t.Fatalf("expected nil credential, got %+v", c)
	}
	list, err := s.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("List on absent file: %v, %v", list, err)
	}

	// Modify stores a credential and the file is 0600.
	err = s.Modify("anthropic", func(current *Credential) (*Credential, error) {
		if current != nil {
			t.Fatalf("expected nil current on first write")
		}
		return &Credential{APIKey: &APIKeyCredential{Key: "sk-test"}}, nil
	})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}

	got, err := s.Read("anthropic")
	if err != nil || got == nil || got.APIKey == nil || got.APIKey.Key != "sk-test" {
		t.Fatalf("Read after write: %+v, %v", got, err)
	}

	// nil return from fn means "leave unchanged", not delete.
	err = s.Modify("anthropic", func(current *Credential) (*Credential, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Modify no-op: %v", err)
	}
	got, err = s.Read("anthropic")
	if err != nil || got == nil || got.APIKey.Key != "sk-test" {
		t.Fatalf("credential dropped by nil-return modify: %+v, %v", got, err)
	}

	// Delete removes it.
	if err := s.Delete("anthropic"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err = s.Read("anthropic")
	if err != nil || got != nil {
		t.Fatalf("expected nil after delete, got %+v, %v", got, err)
	}
}

func TestFileCredentialStoreOAuthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	err := s.Modify("anthropic-oauth", func(current *Credential) (*Credential, error) {
		return &Credential{OAuth: &OAuthCredential{Refresh: "r", Access: "a", Expires: 12345}}, nil
	})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}
	got, err := s.Read("anthropic-oauth")
	if err != nil || got == nil || got.OAuth == nil || got.OAuth.Refresh != "r" || got.OAuth.Expires != 12345 {
		t.Fatalf("Read: %+v, %v", got, err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].Type != "oauth" {
		t.Fatalf("List: %+v, %v", list, err)
	}
}

func TestFileCredentialStoreOtherReadErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	// Make path a directory so ReadFile fails with something other than
	// ENOENT.
	path := filepath.Join(dir, "credentials.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	s := NewFileCredentialStore(path)
	_, err := s.Read("anthropic")
	if err == nil {
		t.Fatal("expected an error reading a directory as a file")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected a non-ENOENT error, got %v", err)
	}
}

// TestFileCredentialStoreSerializesPerProvider exercises concurrent Modify
// calls on the same provider and asserts no write is lost -- the property
// the per-provider mutex chain exists to guarantee.
func TestFileCredentialStoreSerializesPerProvider(t *testing.T) {
	dir := t.TempDir()
	s := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.Modify("p", func(current *Credential) (*Credential, error) {
				key := "k"
				if current != nil && current.APIKey != nil {
					key = current.APIKey.Key + "x"
				}
				return &Credential{APIKey: &APIKeyCredential{Key: key}}, nil
			})
		}(i)
	}
	wg.Wait()

	got, err := s.Read("p")
	if err != nil || got == nil || got.APIKey == nil {
		t.Fatalf("Read: %+v, %v", got, err)
	}
	if len(got.APIKey.Key) != n {
		t.Fatalf("expected %d serialized appends, got key %q (len %d) -- a lost write means the mutex chain is broken",
			n, got.APIKey.Key, len(got.APIKey.Key))
	}
}
