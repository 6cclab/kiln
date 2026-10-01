package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// benchPerms is 80 deny rules of mixed shapes, as a large real settings
// file might hold.
func benchPerms() Permissions {
	var p Permissions
	for i := 0; i < 20; i++ {
		p.Deny = append(p.Deny,
			fmt.Sprintf("Read(secret%d/**)", i),
			fmt.Sprintf("Edit(//etc/conf%d/**)", i),
			fmt.Sprintf("Read(~/.creds%d)", i),
			fmt.Sprintf("Bash(tool%d *)", i))
	}
	return p
}

// The DecideIn benchmarks are what the permission gate runs: it always
// passes its primary root as cwd. Decide("") also pays for os.Getwd.
func BenchmarkDecideIn80DenyEdit(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	cwd := b.TempDir()
	p := benchPerms()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DecideIn(p, cwd, "edit", "src/app/main.go", ModeAcceptEdits)
	}
}

func BenchmarkDecideIn80DenyBash(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	cwd := b.TempDir()
	p := benchPerms()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DecideIn(p, cwd, "bash", "cd src && cat a.go b.go | grep x > out.txt", ModeAuto)
	}
}

func BenchmarkDecide80DenyEdit(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	cwd := b.TempDir()
	b.Chdir(cwd)
	p := benchPerms()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Decide(p, "edit", "src/app/main.go", ModeAcceptEdits)
	}
}

func BenchmarkDecide80DenyBash(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	cwd := b.TempDir()
	b.Chdir(cwd)
	p := benchPerms()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Decide(p, "bash", "cd src && cat a.go b.go | grep x > out.txt", ModeAuto)
	}
}

// catOperands is "cat f0.txt … f199.txt" over files that exist in dir.
func catOperands(tb testing.TB, dir string) string {
	tb.Helper()
	cmd := "cat"
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			tb.Fatal(err)
		}
		cmd += " " + name
	}
	return cmd
}

// BenchmarkDecideIn80DenyCat200 is a cat of 200 existing files.
func BenchmarkDecideIn80DenyCat200(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	cwd := b.TempDir()
	cmd := catOperands(b, cwd)
	p := benchPerms()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DecideIn(p, cwd, "bash", cmd, ModeAuto)
	}
}

// BenchmarkDecideIn80DenyTouch200 creates 200 files that do not exist
// yet: each takes its directory's canonical name.
func BenchmarkDecideIn80DenyTouch200(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	cwd := b.TempDir()
	cmd := "touch"
	for i := 0; i < 200; i++ {
		cmd += fmt.Sprintf(" n%d.txt", i)
	}
	p := benchPerms()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DecideIn(p, cwd, "bash", cmd, ModeAuto)
	}
}

// TestCanonicalOncePerOperand (verification of 12ce170, LOW 6): one
// decision asks the kernel for each path's name at most once, however many
// rules and spellings compare it.
func TestCanonicalOncePerOperand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	// 200 files that exist, then 200 that do not: each missing one takes
	// its directory's canonical name, which must be asked once, not 200
	// times.
	cmd := catOperands(t, cwd)
	for i := 0; i < 200; i++ {
		cmd += fmt.Sprintf(" missing/m%d.txt", i)
	}
	if err := os.Mkdir(filepath.Join(cwd, "missing"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	orig := kernelPath
	kernelPath = func(p string) (string, bool) { calls[p]++; return orig(p) }
	t.Cleanup(func() { kernelPath = orig })
	DecideIn(benchPerms(), cwd, "bash", cmd, ModeAuto)
	total := 0
	for p, n := range calls {
		total += n
		if n > 1 {
			t.Errorf("%s asked %d times", p, n)
		}
	}
	if total == 0 && runtime.GOOS == "darwin" {
		t.Error("the kernel was never asked: the hook is not on the path")
	}
}
