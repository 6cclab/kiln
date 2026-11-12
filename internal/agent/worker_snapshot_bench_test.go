package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkWorkerSnapshot isolates local snapshot setup from model work and
// worktree checkout. Run with -benchtime=1x to measure the maximum-size case.
func BenchmarkWorkerSnapshot(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("files_%d", count), func(b *testing.B) {
			repo, home := b.TempDir(), b.TempDir()
			b.Setenv("HOME", home)
			git := func(args ...string) []byte {
				b.Helper()
				cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
				cmd.Dir = repo
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
				out, err := cmd.CombinedOutput()
				if err != nil {
					b.Fatalf("git %v: %v: %s", args, err, out)
				}
				return out
			}
			git("init", "-q")
			git("-c", "user.name=Kiln Benchmark", "-c", "user.email=bench@example.invalid", "commit", "--allow-empty", "-qm", "base")
			head := strings.TrimSpace(string(git("rev-parse", "HEAD")))
			for i := 0; i < count; i++ {
				if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("file-%04d.txt", i)), []byte("snapshot contents\n"), 0600); err != nil {
					b.Fatal(err)
				}
			}
			b.ResetTimer()
			for b.Loop() {
				if _, err := captureWorkerSnapshot(context.Background(), repo, head); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
