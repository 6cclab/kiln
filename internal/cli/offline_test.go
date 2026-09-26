package cli

import "testing"

func TestOfflineGuard(t *testing.T) {
	t.Setenv("HARNESS_OFFLINE", "")
	if err := offlineGuard("anthropic"); err != nil {
		t.Fatalf("guard off: unexpected %v", err)
	}
	t.Setenv("HARNESS_OFFLINE", "1")
	for _, p := range []string{"faux", "ollama"} {
		if err := offlineGuard(p); err != nil {
			t.Fatalf("%s should be allowed offline: %v", p, err)
		}
	}
	for _, p := range []string{"anthropic", "openai", "bedrock"} {
		if err := offlineGuard(p); err == nil {
			t.Fatalf("%s should be refused offline", p)
		}
	}
}
