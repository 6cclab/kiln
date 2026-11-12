package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerificationWindowsCrossVolumePathSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, target, targetRoot string
	}{
		{"drive file", `D:\replace\file.go`, `D:\`},
		{"UNC directory", `\\server\share\replace`, `\\server\share\`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const source = `C:\module`
			const sourceRoot = `C:\`
			if _, err := filepath.Rel(source, tc.target); err == nil {
				t.Fatal("different volumes unexpectedly have a relative path")
			}
			if got := filepath.Dir(source); got != sourceRoot {
				t.Fatalf("source parent = %q, want %q", got, sourceRoot)
			}
			if got := filepath.Dir(tc.targetRoot); got != tc.targetRoot {
				t.Fatalf("volume root parent = %q, want %q", got, tc.targetRoot)
			}
			stop := verificationCommonAncestor(source, tc.target)
			if stop != sourceRoot {
				t.Fatalf("fallback ancestor = %q, want %q", stop, sourceRoot)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := verificationWalkAncestors(ctx, tc.target, stop, filepath.Dir, func(string) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "across volumes") {
				t.Fatalf("expected unavailable cross-volume evidence, got %v", err)
			}
		})
	}
}
