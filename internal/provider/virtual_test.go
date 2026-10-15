package provider

import "testing"

func TestIsVirtual(t *testing.T) {
	if !IsVirtual(Model{Api: ApiVirtual}) {
		t.Error("IsVirtual(ApiVirtual model) = false")
	}
	if IsVirtual(Model{Api: ApiAnthropicMessages}) {
		t.Error("IsVirtual(anthropic model) = true")
	}
}

func TestClampThinkingLevel(t *testing.T) {
	str := func(s string) *string { return &s }
	plain := Model{ID: "plain"}
	reasoning := Model{ID: "reasoning", Reasoning: true}
	withXHigh := Model{ID: "xhigh", Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{ThinkingXHigh: str("xhigh")}}
	withMax := Model{ID: "max", Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{ThinkingMax: str("max")}}
	noMinimal := Model{ID: "no-minimal", Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{ThinkingMinimal: nil}}
	noLow := Model{ID: "no-low", Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{ThinkingMinimal: nil, ThinkingLow: nil}}
	alwaysOn := Model{ID: "always-on", Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{ThinkingOff: nil, ThinkingMinimal: nil}}

	cases := []struct {
		m     Model
		level ThinkingLevel
		want  ThinkingLevel
	}{
		{plain, "", ""},
		{plain, ThinkingHigh, ThinkingOff},
		{plain, ThinkingOff, ThinkingOff},
		{reasoning, "", ""},
		{reasoning, ThinkingOff, ThinkingOff},
		{reasoning, ThinkingMedium, ThinkingMedium},
		{reasoning, ThinkingHigh, ThinkingHigh},
		// xhigh and max need an explicit mapping: step down to high.
		{reasoning, ThinkingXHigh, ThinkingHigh},
		{reasoning, ThinkingMax, ThinkingHigh},
		{withXHigh, ThinkingXHigh, ThinkingXHigh},
		{withXHigh, ThinkingMax, ThinkingXHigh},
		// max is mapped but xhigh is not: xhigh steps down, not up.
		{withMax, ThinkingXHigh, ThinkingHigh},
		{withMax, ThinkingMax, ThinkingMax},
		// A level mapped to null is unsupported: step down past it.
		{noMinimal, ThinkingMinimal, ThinkingOff},
		{noLow, ThinkingLow, ThinkingOff},
		// Nothing supported below: step up to the nearest supported.
		{alwaysOn, ThinkingOff, ThinkingLow},
		{alwaysOn, ThinkingMinimal, ThinkingLow},
		// Not a level: the lowest the model supports.
		{reasoning, "bogus", ThinkingOff},
		{alwaysOn, "bogus", ThinkingLow},
	}
	for _, c := range cases {
		if got := ClampThinkingLevel(c.m, c.level); got != c.want {
			t.Errorf("ClampThinkingLevel(%s, %q) = %q, want %q", c.m.ID, c.level, got, c.want)
		}
	}
}
