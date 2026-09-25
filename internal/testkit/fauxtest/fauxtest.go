// Package fauxtest provides the single shared helper for starting an
// internal/testkit/faux scripted model server in a Go test, replacing four
// near-identical copies that had accreted across internal/cli,
// internal/provider/api and test/e2e. It carries no build tag so both the
// plain and e2e-tagged test packages can import it.
package fauxtest

import (
	"testing"

	"github.com/andrepato/harness/internal/testkit/faux"
)

// Start starts a scripted faux server loaded from scriptYAML, registers a
// t.Cleanup to close it, and returns its listen address (host:port, no
// scheme) plus the server itself so a caller can inspect Requests(), call
// Reset(), or load a different script mid-test via LoadScriptYAML.
//
// t is testing.TB (not *testing.T) so both ordinary tests and benchmarks
// can use it.
func Start(t testing.TB, scriptYAML string) (addr string, srv *faux.Server) {
	t.Helper()
	srv, err := faux.New(faux.Options{ScriptYAML: scriptYAML})
	if err != nil {
		t.Fatalf("fauxtest: faux.New: %v", err)
	}
	addr, err = srv.Start()
	if err != nil {
		t.Fatalf("fauxtest: srv.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return addr, srv
}
