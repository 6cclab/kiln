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
