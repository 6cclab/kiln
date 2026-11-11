package agent

import "testing"

func TestDispatchLimitsNoNestedDeadlock(t *testing.T) {
	l := &DispatchLimits{Concurrent: 1, Dispatches: 2, Depth: 2}
	release, err := l.acquire(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.acquire(2); err == nil {
		t.Fatal("nested work should fail immediately at capacity")
	}
	release()
	release()
	if _, err = l.acquire(3); err == nil {
		t.Fatal("depth exceeded")
	}
	release, err = l.acquire(2)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err = l.acquire(1); err == nil {
		t.Fatal("dispatch budget exceeded")
	}
}
