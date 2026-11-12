package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

// A command that changes a declared input while it runs and puts it back
// leaves identical before/after fingerprints, yet its check ran against other
// inputs. It records the attempt as a mutation check and no proof.
func TestVerificationBashToolMutateAndRestoreRecordsNoProof(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "source.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "source.bak")
	wrapped := VerificationBashTool(BashTool(execenv.New(cwd)), func(path string) bool { return path == "source.go" })
	result := runVerification(t, wrapped, cwd, `cp source.go `+backup+` && echo broken > source.go && sh -c 'exit 0' && cp `+backup+` source.go`)
	if proof, ok := VerificationFromResult(result.Details); ok {
		t.Fatalf("mutate-and-restore check recorded a verification proof: %+v", proof)
	}
}

func TestCommandMutatesFiles(t *testing.T) {
	for command, want := range map[string]string{
		"go test ./...":                         "",
		"go test ./... > /dev/null 2>&1":        "",
		"go test ./... 2>&1 | tee /tmp/log":     "tee",
		"go test ./... > test.log":              "output redirect to a file",
		"sed -i 's/a/b/' f.go && go test ./...": "sed in-place edit",
		"sed -n 1,5p f.go":                      "",
		"perl -0pi -e 's/a/b/' f.go":            "perl in-place edit",
		"git show HEAD:f.go > f.go":             "output redirect to a file",
		"git checkout -- f.go":                  "git checkout",
		"git status && git diff":                "",
		"cp f.go /tmp/b && go test ./x":         "cp",
		"rm -f coverage.out && go test ./...":   "rm",
		"if true; then":                         "unparseable shell syntax",
	} {
		if got := CommandMutatesFiles(command); got != want {
			t.Errorf("CommandMutatesFiles(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestCommandRunsCheck(t *testing.T) {
	for command, want := range map[string]bool{
		"go test ./...":                       true,
		"cd sub && go vet ./...":              true,
		"go run ./cmd/x":                      false,
		"make check":                          true,
		"npm test":                            true,
		"npm run lint":                        true,
		"npm run dev":                         false,
		"pnpm typecheck":                      true,
		"python3 -m pytest -q":                true,
		"python3 script.py":                   false,
		"cargo clippy":                        true,
		"./gradlew test":                      true,
		"bundle exec rspec":                   true,
		"ls -la":                              false,
		"sh -c 'go test ./...'":               false, // nested strings are not inspected
		"go test ./... 2>&1 | tail -5":        true,
		"sed -i 's/a/b/' f.go && go test ./x": true,
	} {
		if got := CommandRunsCheck(command); got != want {
			t.Errorf("CommandRunsCheck(%q) = %t, want %t", command, got, want)
		}
	}
}
