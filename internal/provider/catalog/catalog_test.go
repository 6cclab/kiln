package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVendoredFileCount(t *testing.T) {
	entries, err := os.ReadDir("data")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	if n != 41 {
		t.Fatalf("vendored provider files = %d, want 41", n)
	}
}

// TestModelCountMatchesVendoredJSON computes the expected model count
// directly from the vendored JSON (not hardcoded) and asserts the loaded
// catalog agrees, logging the count either way.
func TestModelCountMatchesVendoredJSON(t *testing.T) {
	entries, err := os.ReadDir("data")
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join("data", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var file map[string]map[string]json.RawMessage
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		for _, models := range file {
			want += len(models)
		}
	}
	got := Count()
	t.Logf("computed model count from vendored JSON: %d", want)
	if got != want {
		t.Fatalf("catalog.Count() = %d, want %d (computed from vendored data/*.json)", got, want)
	}
	if got == 0 {
		t.Fatal("catalog loaded zero models")
	}
}

func TestVersionNonEmpty(t *testing.T) {
	v := Version()
	if v == "" {
		t.Fatal("catalog.Version() is empty")
	}
	t.Logf("catalog version: %s", v)
}

func TestLookupAnthropic(t *testing.T) {
	all := All()
	models, ok := all["anthropic"]
	if !ok || len(models) == 0 {
		t.Fatal("expected anthropic provider models in catalog")
	}
	m, ok := Lookup("anthropic", models[0].ID)
	if !ok {
		t.Fatalf("Lookup(anthropic, %s) not found", models[0].ID)
	}
	if m.ID != models[0].ID {
		t.Fatalf("Lookup returned %q, want %q", m.ID, models[0].ID)
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, ok := Lookup("nope", "nope"); ok {
		t.Fatal("expected Lookup to report false for an unknown provider/model")
	}
}
