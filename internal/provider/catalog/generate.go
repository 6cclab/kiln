//go:build ignore

// Run with: PI_AI_DIR=/path/to/pi-ai/package go run generate.go
//
// Copies dist/providers/data/*.json from a checked-out pi-ai package into
// ./data/, and writes VERSION from that package's package.json. Invoked via
// `go generate` (see the directive in catalog.go), but not part of the
// normal build (the ignore tag keeps it out of `go build ./...`).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	piDir := os.Getenv("PI_AI_DIR")
	if piDir == "" {
		fmt.Fprintln(os.Stderr, "generate: PI_AI_DIR must point at a pi-ai package directory (containing dist/ and package.json)")
		os.Exit(1)
	}

	srcData := filepath.Join(piDir, "dist", "providers", "data")
	entries, err := os.ReadDir(srcData)
	must(err)

	outDir := "data"
	must(os.MkdirAll(outDir, 0o755))

	// Remove stale vendored files so a provider dropped upstream does not
	// linger in the catalog.
	existing, _ := os.ReadDir(outDir)
	for _, e := range existing {
		must(os.Remove(filepath.Join(outDir, e.Name())))
	}

	n := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		must(copyFile(filepath.Join(srcData, e.Name()), filepath.Join(outDir, e.Name())))
		n++
	}
	fmt.Printf("generate: copied %d provider files from %s\n", n, srcData)

	pkgRaw, err := os.ReadFile(filepath.Join(piDir, "package.json"))
	must(err)
	var pkg struct {
		Version string `json:"version"`
	}
	must(json.Unmarshal(pkgRaw, &pkg))
	if pkg.Version == "" {
		fmt.Fprintln(os.Stderr, "generate: pi-ai package.json has no version field")
		os.Exit(1)
	}
	must(os.WriteFile("VERSION", []byte(pkg.Version+"\n"), 0o644))
	fmt.Printf("generate: wrote VERSION = %s\n", pkg.Version)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
}
