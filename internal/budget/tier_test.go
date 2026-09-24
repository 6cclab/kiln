package budget

import "testing"

func TestTierForWindow(t *testing.T) {
	cases := []struct {
		window       int
		wantName     string
		wantStrategy ToolStrategy
	}{
		{32_768, "small", StrategyPostureIndex},
		{49_152, "medium", StrategyFullIndex},
		{131_072, "medium", StrategyFullIndex},
		{200_000, "large", StrategyFullSchemas},
		{1_000_000, "large", StrategyFullSchemas},
	}
	for _, c := range cases {
		tier := TierForWindow(c.window)
		if tier.Name != c.wantName {
			t.Errorf("window %d: name = %q, want %q", c.window, tier.Name, c.wantName)
		}
		if tier.ToolStrategy != c.wantStrategy {
			t.Errorf("window %d: strategy = %q, want %q", c.window, tier.ToolStrategy, c.wantStrategy)
		}
		if tier.ContextWindow != c.window {
			t.Errorf("window %d: ContextWindow = %d", c.window, tier.ContextWindow)
		}
	}
}

// TestMonotonicUsable asserts the bug the proportional fractions fixed: a
// 49,152-token window must have MORE usable tokens than a 32,768-token
// window. An earlier absolute-budget scheme inverted this (25,338 usable at
// 32,768 vs 21,075 usable at 49,152).
func TestMonotonicUsable(t *testing.T) {
	small := TierForWindow(32_768)
	medium := TierForWindow(49_152)
	usableSmall := UsableTokens(small)
	usableMedium := UsableTokens(medium)
	if usableMedium <= usableSmall {
		t.Fatalf("usable(49152)=%d must be > usable(32768)=%d", usableMedium, usableSmall)
	}
}

func TestUsableTokensAcrossWindows(t *testing.T) {
	for _, w := range []int{32_768, 49_152, 131_072, 200_000, 1_000_000} {
		tier := TierForWindow(w)
		usable := UsableTokens(tier)
		t.Logf("window=%d tier=%s strategy=%s systemPrompt=%d reserve=%d usable=%d",
			w, tier.Name, tier.ToolStrategy, tier.SystemPromptTokens, tier.Compaction.ReserveTokens, usable)
		if usable <= 0 {
			t.Errorf("window %d: usable tokens must be positive, got %d", w, usable)
		}
	}
}

func TestRequireTierForWindowTooSmall(t *testing.T) {
	_, err := RequireTierForWindow(1024)
	if err == nil {
		t.Fatal("expected ContextTooSmallError for a 1024-token window")
	}
	var tooSmall *ContextTooSmallError
	if !asContextTooSmall(err, &tooSmall) {
		t.Fatalf("expected *ContextTooSmallError, got %T: %v", err, err)
	}
	if tooSmall.Shortfall <= 0 {
		t.Errorf("shortfall must be positive, got %d", tooSmall.Shortfall)
	}
	if tooSmall.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func asContextTooSmall(err error, target **ContextTooSmallError) bool {
	if e, ok := err.(*ContextTooSmallError); ok {
		*target = e
		return true
	}
	return false
}

func TestRequireTierForWindowOK(t *testing.T) {
	tier, err := RequireTierForWindow(131_072)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier.Name != "medium" {
		t.Errorf("tier.Name = %q, want medium", tier.Name)
	}
}
