package settings

import (
	"path/filepath"
	"testing"
)

func TestAutoIntegrateRequiresUserScopeOptIn(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".kiln", "settings.json")
	project := filepath.Join(cwd, ".claude", "settings.json")
	put(t, project, `{"orchestration":{"autoIntegrate":true}}`)
	got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true})
	if got.Orchestration.AutoIntegrate != nil {
		t.Fatal("repository enabled unattended integration")
	}
	put(t, user, `{"orchestration":{"autoIntegrate":true}}`)
	got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true})
	if got.Orchestration.AutoIntegrate == nil || !*got.Orchestration.AutoIntegrate {
		t.Fatal("user opt-in did not enable integration")
	}
	if got.EfficiencyProvenance()["orchestration.autoIntegrate"] != user {
		t.Fatal("rejected project setting took provenance")
	}
	put(t, project, `{"orchestration":{"autoIntegrate":false}}`)
	got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true})
	if got.Orchestration.AutoIntegrate == nil || *got.Orchestration.AutoIntegrate {
		t.Fatal("repository could not disable user integration opt-in")
	}
	put(t, user, `{"orchestration":{"autoIntegrate":false}}`)
	put(t, project, `{"orchestration":{"autoIntegrate":true}}`)
	got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true})
	if got.Orchestration.AutoIntegrate == nil || *got.Orchestration.AutoIntegrate {
		t.Fatal("repository undid explicit user opt-out")
	}
	extra := filepath.Join(t.TempDir(), "extra-settings.json")
	put(t, extra, `{"orchestration":{"autoIntegrate":true}}`)
	got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true, Extra: extra})
	if got.Orchestration.AutoIntegrate == nil || *got.Orchestration.AutoIntegrate {
		t.Fatal("extra settings file undid explicit user opt-out")
	}
}

func TestOrchestrationAutoIntegrateMerge(t *testing.T) {
	yes, no := true, false
	current := Orchestration{AutoIntegrate: &yes}
	current.merge(Orchestration{})
	if current.AutoIntegrate == nil || !*current.AutoIntegrate {
		t.Fatal("absent setting replaced autoIntegrate")
	}
	current.merge(Orchestration{AutoIntegrate: &no})
	if current.AutoIntegrate == nil || *current.AutoIntegrate {
		t.Fatal("explicit false did not override autoIntegrate")
	}
}

func TestOrchestrationAutoIntegrateProvenance(t *testing.T) {
	yes := true
	s := Settings{}
	before := efficiencyPointers(s)
	s.Orchestration.merge(Orchestration{AutoIntegrate: &yes})
	s.recordEfficiencySources(before, rawSettings{}, "project settings")
	if got := s.EfficiencySources["orchestration.autoIntegrate"]; got != "project settings" {
		t.Fatalf("autoIntegrate source = %q", got)
	}
}

func TestOrchestrationIsolatedWorkersMerge(t *testing.T) {
	yes, no := true, false
	current := Orchestration{IsolatedWorkers: &yes}
	current.merge(Orchestration{})
	if current.IsolatedWorkers == nil || !*current.IsolatedWorkers {
		t.Fatal("omitted overlay changed isolation")
	}
	current.merge(Orchestration{IsolatedWorkers: &no})
	if current.IsolatedWorkers == nil || *current.IsolatedWorkers {
		t.Fatal("explicit false did not disable isolation")
	}
}

func TestOrchestrationSnapshotDirtyMerge(t *testing.T) {
	yes, no := true, false
	current := Orchestration{SnapshotDirty: &yes}
	current.merge(Orchestration{})
	if current.SnapshotDirty == nil || !*current.SnapshotDirty {
		t.Fatal("omitted overlay changed snapshot opt-in")
	}
	current.merge(Orchestration{SnapshotDirty: &no})
	if current.SnapshotDirty == nil || *current.SnapshotDirty {
		t.Fatal("explicit false did not disable snapshots")
	}
}

func TestOrchestrationSnapshotDirtyProvenance(t *testing.T) {
	s := Settings{}
	before := efficiencyPointers(s)
	yes := true
	s.Orchestration.merge(Orchestration{SnapshotDirty: &yes})
	s.recordEfficiencySources(before, rawSettings{}, "project settings")
	if got := s.EfficiencySources["orchestration.snapshotDirty"]; got != "project settings" {
		t.Fatalf("source = %q", got)
	}
	if s.Orchestration.SnapshotDirty == nil || !*s.Orchestration.SnapshotDirty {
		t.Fatal("snapshot setting did not load")
	}
}

func TestOrchestrationIsolatedShellMergeAndProvenance(t *testing.T) {
	yes, no := true, false
	s := Settings{}
	before := efficiencyPointers(s)
	s.Orchestration.merge(Orchestration{IsolatedShell: &yes})
	s.recordEfficiencySources(before, rawSettings{}, "user settings")
	if s.Orchestration.IsolatedShell == nil || !*s.Orchestration.IsolatedShell || s.EfficiencySources["orchestration.isolatedShell"] != "user settings" {
		t.Fatalf("isolated shell setting or source lost: %+v, %+v", s.Orchestration, s.EfficiencySources)
	}
	s.Orchestration.merge(Orchestration{})
	if !*s.Orchestration.IsolatedShell {
		t.Fatal("absent overlay disabled isolated shell")
	}
	s.Orchestration.merge(Orchestration{IsolatedShell: &no})
	if *s.Orchestration.IsolatedShell {
		t.Fatal("explicit false did not disable isolated shell")
	}
}

func TestIsolatedShellRequiresUserScopeOptIn(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".kiln", "settings.json")
	project := filepath.Join(cwd, ".claude", "settings.json")
	put(t, project, `{"orchestration":{"isolatedShell":true}}`)
	if got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true}); got.Orchestration.IsolatedShell != nil {
		t.Fatal("repository enabled shell-capable workers")
	}
	extra := filepath.Join(t.TempDir(), "extra-settings.json")
	put(t, extra, `{"orchestration":{"isolatedShell":true}}`)
	if got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true, Extra: extra}); got.Orchestration.IsolatedShell != nil {
		t.Fatal("extra settings file enabled shell-capable workers")
	}
	put(t, user, `{"orchestration":{"isolatedShell":true}}`)
	got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true})
	if got.Orchestration.IsolatedShell == nil || !*got.Orchestration.IsolatedShell {
		t.Fatal("user opt-in did not enable shell-capable workers")
	}
	put(t, project, `{"orchestration":{"isolatedShell":false}}`)
	got = LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true})
	if got.Orchestration.IsolatedShell == nil || *got.Orchestration.IsolatedShell {
		t.Fatal("repository could not turn the user opt-in off")
	}
}
