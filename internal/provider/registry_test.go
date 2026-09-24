package provider_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
)

func TestRegistryResolveAndAvailable(t *testing.T) {
	s, err := tkfaux.New(tkfaux.Options{ScriptYAML: `
model: faux-1
steps:
  - text: "hi from registry test"
`})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	t.Setenv("HARNESS_FAUX_ADDR", addr)

	fp, ok := fauxprovider.New()
	if !ok {
		t.Fatal("expected faux provider to register with HARNESS_FAUX_ADDR set")
	}

	store := auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	reg := provider.NewRegistry(store)
	reg.Register(fp)

	m, ok := reg.GetModel("faux", "faux-1")
	if !ok {
		t.Fatal("GetModel(faux, faux-1) not found")
	}
	if m.ContextWindow != 32768 {
		t.Fatalf("ContextWindow = %d", m.ContextWindow)
	}

	resolved, err := reg.Resolve("faux", "faux-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Tier.Name != "small" {
		t.Fatalf("Tier.Name = %q, want small", resolved.Tier.Name)
	}
	if resolved.Suppression.Suffix != "" {
		t.Fatalf("expected no suppression for a non-reasoning model, got %+v", resolved.Suppression)
	}

	ctx := context.Background()
	authed, err := reg.CheckAuth(ctx, "faux")
	if err != nil || !authed {
		t.Fatalf("CheckAuth(faux) = %v, %v; faux has AuthKindNone and should always report true", authed, err)
	}
	available, err := reg.Available(ctx)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if len(available) != 1 || available[0].ID() != "faux" {
		t.Fatalf("Available() = %+v", available)
	}

	_, err = reg.Resolve("faux", "nonexistent-model")
	if err == nil {
		t.Fatal("expected an error resolving an unknown model")
	}
}
