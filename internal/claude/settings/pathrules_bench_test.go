package settings

import (
	"fmt"
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
