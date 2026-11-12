package settings

import "testing"

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
