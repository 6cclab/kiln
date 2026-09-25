package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syntheticProfile has two packages: internal/good at 100% (2/2 statements
// covered) and internal/bad at 50% (1/2 statements covered).
const syntheticProfile = `mode: atomic
github.com/andrepato/harness/internal/good/a.go:1.1,3.2 1 1
github.com/andrepato/harness/internal/good/b.go:1.1,3.2 1 1
github.com/andrepato/harness/internal/bad/a.go:1.1,3.2 1 1
github.com/andrepato/harness/internal/bad/b.go:1.1,3.2 1 0
`

func TestParseProfile(t *testing.T) {
	pkgs, err := parseProfile(strings.NewReader(syntheticProfile))
	if err != nil {
		t.Fatalf("parseProfile: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %d packages, want 2: %+v", len(pkgs), pkgs)
	}
	good := pkgs["internal/good"]
	if good == nil || good.total != 2 || good.covered != 2 {
		t.Fatalf("internal/good = %+v, want total=2 covered=2", good)
	}
	if got := good.percent(); got != 100 {
		t.Errorf("internal/good.percent() = %v, want 100", got)
	}
	bad := pkgs["internal/bad"]
	if bad == nil || bad.total != 2 || bad.covered != 1 {
		t.Fatalf("internal/bad = %+v, want total=2 covered=1", bad)
	}
	if got := bad.percent(); got != 50 {
		t.Errorf("internal/bad.percent() = %v, want 50", got)
	}
}

func TestParseFloors(t *testing.T) {
	input := `# comment line, ignored

internal/good	95.0
internal/bad	60.0
`
	floors, err := parseFloors(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseFloors: %v", err)
	}
	if len(floors) != 2 {
		t.Fatalf("got %d floors, want 2: %+v", len(floors), floors)
	}
	if floors["internal/good"] != 95.0 {
		t.Errorf("internal/good floor = %v, want 95.0", floors["internal/good"])
	}
	if floors["internal/bad"] != 60.0 {
		t.Errorf("internal/bad floor = %v, want 60.0", floors["internal/bad"])
	}
}

func TestCheckFloorsBelowFloorFails(t *testing.T) {
	pkgs, err := parseProfile(strings.NewReader(syntheticProfile))
	if err != nil {
		t.Fatalf("parseProfile: %v", err)
	}
	// internal/good is at 100%, floor 95 -> passes.
	// internal/bad is at 50%, floor 60 -> fails.
	floors := map[string]float64{
		"internal/good": 95.0,
		"internal/bad":  60.0,
	}
	var buf bytes.Buffer
	failing := checkFloors(&buf, pkgs, floors)
	if len(failing) != 1 || failing[0] != "internal/bad" {
		t.Fatalf("failing = %v, want [internal/bad]", failing)
	}
	if !strings.Contains(buf.String(), "internal/bad") {
		t.Errorf("output missing failing package: %q", buf.String())
	}
	if strings.Contains(buf.String(), "FAIL internal/good") {
		t.Errorf("output unexpectedly flags passing package: %q", buf.String())
	}
}

func TestCheckFloorsUnlistedPackageNeverFails(t *testing.T) {
	pkgs, err := parseProfile(strings.NewReader(syntheticProfile))
	if err != nil {
		t.Fatalf("parseProfile: %v", err)
	}
	// Only internal/good is listed; internal/bad (50%, well below any
	// reasonable floor) must never appear in the failing set.
	floors := map[string]float64{"internal/good": 50.0}
	var buf bytes.Buffer
	failing := checkFloors(&buf, pkgs, floors)
	if len(failing) != 0 {
		t.Fatalf("failing = %v, want none (internal/bad is unlisted)", failing)
	}
}

func TestPackageOf(t *testing.T) {
	cases := map[string]string{
		"github.com/andrepato/harness/internal/tool/bash.go": "internal/tool",
		"github.com/andrepato/harness/cmd/kiln/main.go":      "cmd/kiln",
		"github.com/andrepato/harness/main.go":               ".",
	}
	for file, want := range cases {
		if got := packageOf(file); got != want {
			t.Errorf("packageOf(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestRunReport(t *testing.T) {
	dir := t.TempDir()
	profilePath := writeTemp(t, dir, "coverage.out", syntheticProfile)

	var stdout, stderr bytes.Buffer
	rc := run([]string{"-profile", profilePath, "-report"}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("run() = %d, want 0; stderr=%q", rc, stderr.String())
	}
	if !strings.Contains(stdout.String(), "internal/good") || !strings.Contains(stdout.String(), "internal/bad") {
		t.Errorf("report output missing packages: %q", stdout.String())
	}
}

func TestRunFloorsFailingExitsNonzero(t *testing.T) {
	dir := t.TempDir()
	profilePath := writeTemp(t, dir, "coverage.out", syntheticProfile)
	floorsPath := writeTemp(t, dir, ".coverage-floors", "internal/bad\t60.0\ninternal/good\t95.0\n")

	var stdout, stderr bytes.Buffer
	rc := run([]string{"-profile", profilePath, "-floors", floorsPath}, &stdout, &stderr)
	if rc != 1 {
		t.Fatalf("run() = %d, want 1; stdout=%q stderr=%q", rc, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "internal/bad") {
		t.Errorf("expected failing package named in output: %q", stdout.String())
	}
}

func TestRunFloorsPassingExitsZero(t *testing.T) {
	dir := t.TempDir()
	profilePath := writeTemp(t, dir, "coverage.out", syntheticProfile)
	floorsPath := writeTemp(t, dir, ".coverage-floors", "internal/good\t95.0\n")

	var stdout, stderr bytes.Buffer
	rc := run([]string{"-profile", profilePath, "-floors", floorsPath}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("run() = %d, want 0; stdout=%q stderr=%q", rc, stdout.String(), stderr.String())
	}
}

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%s): %v", p, err)
	}
	return p
}
