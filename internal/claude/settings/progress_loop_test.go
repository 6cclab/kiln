package settings

import (
	"path/filepath"
	"testing"
)

func TestProgressLoopLimitDefaultsOffAndProjectCannotEnable(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".kiln", "settings.json")
	project := filepath.Join(cwd, ".claude", "settings.json")
	put(t, project, `{"runtimeEfficiency":{"progressLoopLimit":2}}`)
	got := LoadSettings(cwd, LoadOptions{Trusted: true})
	if got.RuntimeEfficiency.ProgressLoopLimit != nil {
		t.Fatalf("project enabled default-off guard: %+v", got.RuntimeEfficiency)
	}
	put(t, user, `{"runtimeEfficiency":{"progressLoopLimit":0}}`)
	got = LoadSettings(cwd, LoadOptions{Trusted: true})
	if got.RuntimeEfficiency.ProgressLoopLimit == nil || *got.RuntimeEfficiency.ProgressLoopLimit != 0 {
		t.Fatalf("project undid user opt-out: %+v", got.RuntimeEfficiency)
	}
	put(t, user, `{"runtimeEfficiency":{"progressLoopLimit":3}}`)
	got = LoadSettings(cwd, LoadOptions{Trusted: true})
	if got.RuntimeEfficiency.ProgressLoopLimit == nil || *got.RuntimeEfficiency.ProgressLoopLimit != 2 {
		t.Fatalf("user opt-in could not be tightened: %+v", got.RuntimeEfficiency)
	}
}

func TestProgressLoopLimitRejectsInvalidBounds(t *testing.T) {
	for _, invalid := range []int{-1, 1, 11} {
		valid := 3
		cfg := RuntimeEfficiency{ProgressLoopLimit: &valid}
		cfg.merge(RuntimeEfficiency{ProgressLoopLimit: &invalid})
		if *cfg.ProgressLoopLimit != valid {
			t.Fatalf("accepted %d", invalid)
		}
	}
}

func TestProgressLoopFileBytesBoundsAndProjectCannotRaise(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".kiln", "settings.json")
	project := filepath.Join(cwd, ".claude", "settings.json")
	put(t, project, `{"runtimeEfficiency":{"progressLoopFileBytes":8388608}}`)
	if got := LoadSettings(cwd, LoadOptions{Trusted: true}); got.RuntimeEfficiency.ProgressLoopFileBytes != nil {
		t.Fatalf("project raised the cap above the default: %d", *got.RuntimeEfficiency.ProgressLoopFileBytes)
	}
	put(t, user, `{"runtimeEfficiency":{"progressLoopFileBytes":16777216}}`)
	put(t, project, `{"runtimeEfficiency":{"progressLoopFileBytes":8388608}}`)
	got := LoadSettings(cwd, LoadOptions{Trusted: true})
	if v := got.RuntimeEfficiency.ProgressLoopFileBytes; v == nil || *v != 8<<20 {
		t.Fatalf("project could not lower the user cap: %v", v)
	}
	put(t, user, `{"runtimeEfficiency":{"progressLoopFileBytes":33554432}}`)
	put(t, project, `{}`)
	if got := LoadSettings(cwd, LoadOptions{Trusted: true}); got.RuntimeEfficiency.ProgressLoopFileBytes != nil {
		t.Fatalf("accepted a cap above 16 MiB: %d", *got.RuntimeEfficiency.ProgressLoopFileBytes)
	}
}
