package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// defaultPath returns ~/.harness/credentials.json.
func defaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".harness", "credentials.json")
}

// FileCredentialStore is a file-backed credential store.
//
// Two properties the contract demands and that are easy to get wrong:
//
//   - Serialized writes per provider. OAuth refresh is read-modify-write.
//     Two concurrent refreshes racing on the same provider can persist a
//     stale refresh token, which locks you out until you log in again.
//     Writes go through a per-provider mutex.
//   - Atomic replacement. A crash mid-write must not leave a truncated
//     file; that would take out every provider's credentials at once, not
//     just the one being written. Write to a temp file in the same
//     directory, then rename.
//
// This is a Go port of harness/src/auth/credential-store.ts.
type FileCredentialStore struct {
	path string

	mu     sync.Mutex // guards chains
	chains map[string]*sync.Mutex
}

// NewFileCredentialStore builds a store at path, or at the default
// ~/.harness/credentials.json when path is empty.
func NewFileCredentialStore(path string) *FileCredentialStore {
	if path == "" {
		path = defaultPath()
	}
	return &FileCredentialStore{path: path, chains: make(map[string]*sync.Mutex)}
}

type stored map[string]Credential

// load reads the store file. A missing file is the normal first-run state
// and decodes to an empty map. Any other read error propagates: turning it
// into "no credentials" would present as a spurious logout and invite the
// user to re-auth over a file that is merely unreadable.
func (s *FileCredentialStore) load() (stored, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return stored{}, nil
		}
		return nil, err
	}
	var out stored
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = stored{}
	}
	return out, nil
}

// save writes data atomically: temp file in the same directory, then
// rename. 0600 because these are bearer tokens for paid accounts.
func (s *FileCredentialStore) save(data stored) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", s.path, os.Getpid())
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// lockFor returns the per-provider mutex, creating it on first use. Writes
// for the same provider never interleave; writes for different providers
// can proceed concurrently, matching the TS per-provider promise chain.
func (s *FileCredentialStore) lockFor(providerID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.chains[providerID]
	if !ok {
		l = &sync.Mutex{}
		s.chains[providerID] = l
	}
	return l
}

// Read returns the stored credential, possibly expired. nil for a missing
// entry.
func (s *FileCredentialStore) Read(providerID string) (*Credential, error) {
	data, err := s.load()
	if err != nil {
		return nil, err
	}
	if c, ok := data[providerID]; ok {
		return &c, nil
	}
	return nil, nil
}

// List returns stored credential metadata without resolving or exposing
// secrets.
func (s *FileCredentialStore) List() ([]Info, error) {
	data, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(data))
	for providerID, c := range data {
		out = append(out, Info{ProviderID: providerID, Type: c.credentialType()})
	}
	return out, nil
}

// Modify is the only write path: a serialized read-modify-write per
// provider. fn sees the current credential (nil if absent); a nil return
// means "leave unchanged", NOT "delete" -- deletion is Delete. Conflating
// them would drop a credential whenever a refresh decided it had nothing
// new to store.
func (s *FileCredentialStore) Modify(providerID string, fn func(current *Credential) (*Credential, error)) error {
	lock := s.lockFor(providerID)
	lock.Lock()
	defer lock.Unlock()

	data, err := s.load()
	if err != nil {
		return err
	}
	var current *Credential
	if c, ok := data[providerID]; ok {
		current = &c
	}
	next, err := fn(current)
	if err != nil {
		return err
	}
	if next == nil {
		return nil
	}
	data[providerID] = *next
	return s.save(data)
}

// Delete removes a credential (logout). Serialized against Modify for the
// same provider.
func (s *FileCredentialStore) Delete(providerID string) error {
	lock := s.lockFor(providerID)
	lock.Lock()
	defer lock.Unlock()

	data, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := data[providerID]; !ok {
		return nil
	}
	delete(data, providerID)
	return s.save(data)
}
