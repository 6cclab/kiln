// Package catalog embeds pi-ai's generated model catalog
// (dist/providers/data/*.json, 41 provider files, 1,495 models as of the
// vendored VERSION) and exposes it as provider.Model values.
//
// The data is vendored, not fetched at build/run time: `go generate` (via
// generate.go) copies it from a checked-out pi-ai package, and VERSION
// records which pi-ai release it came from.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/provider"
)

//go:generate go run generate.go

//go:embed data/*.json
var dataFS embed.FS

//go:embed VERSION
var versionFile string

// Version is the pi-ai package version the vendored data was copied from.
func Version() string { return strings.TrimSpace(versionFile) }

// rawEntry mirrors one model's JSON shape in a data/*.json file. It is
// decoded straight into provider.Model since the field names and tags
// already match.
type catalogFile map[string]map[string]json.RawMessage // api -> modelId -> raw model

var (
	byProviderAndModel map[string]map[string]provider.Model
	byProvider         map[string][]provider.Model
	loadErr            error
	loaded             bool
)

// load parses every embedded data/*.json file once. Errors are cached and
// returned on every call so a corrupt vendor file fails loudly rather than
// silently producing a truncated catalog.
func load() {
	if loaded {
		return
	}
	loaded = true

	byProviderAndModel = make(map[string]map[string]provider.Model)
	byProvider = make(map[string][]provider.Model)

	entries, err := dataFS.ReadDir("data")
	if err != nil {
		loadErr = fmt.Errorf("catalog: read embedded data dir: %w", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := dataFS.ReadFile("data/" + e.Name())
		if err != nil {
			loadErr = fmt.Errorf("catalog: read %s: %w", e.Name(), err)
			return
		}
		var file catalogFile
		if err := json.Unmarshal(raw, &file); err != nil {
			loadErr = fmt.Errorf("catalog: parse %s: %w", e.Name(), err)
			return
		}
		for _, models := range file {
			for modelID, rawModel := range models {
				var m provider.Model
				if err := json.Unmarshal(rawModel, &m); err != nil {
					loadErr = fmt.Errorf("catalog: parse %s model %s: %w", e.Name(), modelID, err)
					return
				}
				if byProviderAndModel[m.Provider] == nil {
					byProviderAndModel[m.Provider] = make(map[string]provider.Model)
				}
				byProviderAndModel[m.Provider][m.ID] = m
				byProvider[m.Provider] = append(byProvider[m.Provider], m)
			}
		}
	}
	for p := range byProvider {
		sort.Slice(byProvider[p], func(i, j int) bool { return byProvider[p][i].ID < byProvider[p][j].ID })
	}
}

// All returns every catalog model, keyed by provider id.
func All() map[string][]provider.Model {
	load()
	if loadErr != nil {
		panic(loadErr) // vendored data is build-time input; a decode failure is a build defect.
	}
	out := make(map[string][]provider.Model, len(byProvider))
	for k, v := range byProvider {
		cp := make([]provider.Model, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// Lookup returns the model with the given id on the given provider.
func Lookup(providerID, modelID string) (provider.Model, bool) {
	load()
	if loadErr != nil {
		panic(loadErr)
	}
	m, ok := byProviderAndModel[providerID][modelID]
	return m, ok
}

// Count returns the total number of models across every provider, computed
// from the vendored data rather than hardcoded.
func Count() int {
	load()
	if loadErr != nil {
		panic(loadErr)
	}
	n := 0
	for _, v := range byProvider {
		n += len(v)
	}
	return n
}

// ProviderIDs returns every provider id present in the vendored catalog,
// sorted.
func ProviderIDs() []string {
	load()
	if loadErr != nil {
		panic(loadErr)
	}
	out := make([]string, 0, len(byProvider))
	for k := range byProvider {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
