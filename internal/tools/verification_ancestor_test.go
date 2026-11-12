package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/tool"
)

// The parent map models filepath.Dir on Windows. It lets this test exercise
// distinct drive and UNC roots on hosts that cannot mount those volumes.
func TestVerificationCrossVolumeAncestorTraversal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		full    string
		parents map[string]string
		want    []string
	}{
		{
			name: "file on another drive", full: `D:\replace\file.go`,
			parents: map[string]string{
				`D:\replace\file.go`: `D:\replace`,
				`D:\replace`:         `D:\`,
				`D:\`:                `D:\`,
			},
			want: []string{`D:\replace\file.go`, `D:\replace`, `D:\`},
		},
		{
			name: "directory on UNC share", full: `\\server\share\replace`,
			parents: map[string]string{
				`\\server\share\replace`: `\\server\share\`,
				`\\server\share\`:        `\\server\share\`,
			},
			want: []string{`\\server\share\replace`, `\\server\share\`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var visited []string
			err := verificationWalkAncestors(ctx, tc.full, `C:\module`, func(path string) string {
				return tc.parents[path]
			}, func(path string) error {
				visited = append(visited, path)
				if len(visited) > len(tc.want)+1 {
					cancel() // A missing root guard must fail the test without hanging it.
				}
				return nil
			})
			if err == nil || err == context.Canceled || err == context.DeadlineExceeded || !strings.Contains(err.Error(), "across volumes") {
				t.Fatalf("expected unavailable cross-volume evidence, got %v", err)
			}
			if !reflect.DeepEqual(visited, tc.want) {
				t.Fatalf("visited %q, want %q", visited, tc.want)
			}
		})
	}
}

func TestVerificationSameVolumeAncestorTraversal(t *testing.T) {
	var visited []string
	err := verificationWalkAncestors(context.Background(), `C:\module\pkg\file.go`, `C:\module`, func(path string) string {
		switch path {
		case `C:\module\pkg\file.go`:
			return `C:\module\pkg`
		case `C:\module\pkg`:
			return `C:\module`
		default:
			return path
		}
	}, func(path string) error {
		visited = append(visited, path)
		return nil
	})
	if err != nil || !reflect.DeepEqual(visited, []string{`C:\module\pkg\file.go`, `C:\module\pkg`}) {
		t.Fatalf("same-volume traversal: visited=%q err=%v", visited, err)
	}
}

func TestGoVerificationExternalReplaceUnavailableStillRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		deny bool
	}{
		{name: "read denied", deny: true},
		{name: "symlink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			cwd := filepath.Join(parent, "module")
			target := filepath.Join(parent, "external")
			writeGoDiscoveryFixture(t, cwd, "app/app.go", "package app\n")
			writeGoDiscoveryFixture(t, target, "ext.go", "package external\n")
			writeGoDiscoveryFixture(t, cwd, "go.mod", "module example.com/test\nreplace example.com/external => ../external\n")
			if !tc.deny {
				link := filepath.Join(parent, "linked")
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				writeGoDiscoveryFixture(t, cwd, "go.mod", "module example.com/test\nreplace example.com/external => ../linked\n")
			}
			runs := 0
			base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
				runs++
				return tool.Text("ran"), nil
			}}
			deniedPath := filepath.Join("..", "external")
			deniedReads := 0
			allowRead := func(path string) bool {
				if tc.deny && path == deniedPath {
					deniedReads++
					return false
				}
				return true
			}
			result, err := VerificationBashTool(base, allowRead).Execute(context.Background(), json.RawMessage(`{"command":"go test ./app","verificationDiscover":"go"}`), nil, tool.Invocation{Cwd: cwd, ToolCallID: "call"})
			if err != nil || runs != 1 || result.IsError {
				t.Fatalf("command runs once: runs=%d result=%+v err=%v", runs, result, err)
			}
			if tc.deny && deniedReads == 0 {
				t.Fatalf("Read permission was not checked for canonical replacement %q", deniedPath)
			}
			if _, ok := VerificationFromResult(result.Details); ok {
				t.Fatalf("unexpected proof: %s", result.Details)
			}
			attempt, ok := VerificationAttemptFromResult(result.Details)
			if !ok || attempt.DiscoveryUnsupported == "" {
				t.Fatalf("missing unavailable reason: %+v details=%s", attempt, result.Details)
			}
		})
	}
}
