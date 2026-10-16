package provider

// IsVirtual reports whether m is a virtual model (ApiVirtual): one that is
// routed to a physical model per request and never streamed itself, as
// pi's isVirtualModel.
func IsVirtual(m Model) bool { return m.Api == ApiVirtual }

// thinkingLevels is every level in order of effort, as pi-ai's
// EXTENDED_THINKING_LEVELS.
var thinkingLevels = []ThinkingLevel{ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingMax}

// supportsThinkingLevel is pi-ai's getSupportedThinkingLevels for one
// level: a model that does not reason supports only "off"; otherwise a
// level the model's ThinkingLevelMap maps to null is unsupported, and
// "xhigh" and "max" need an explicit mapping.
func supportsThinkingLevel(m Model, level ThinkingLevel) bool {
	if !m.Reasoning {
		return level == ThinkingOff
	}
	mapped, present := m.ThinkingLevelMap[level]
	if present && mapped == nil {
		return false
	}
	if level == ThinkingXHigh || level == ThinkingMax {
		return present
	}
	return true
}

// ClampThinkingLevel fits level to what m supports, for a level a router
// chose without knowing every target's limits. A supported level stands;
// otherwise it steps down to the nearest supported level, and up only when
// nothing below is supported. "" (the model decides) stays "", and a model
// that does not reason gets "off".
//
// pi-ai's clampThinkingLevel steps up first; kiln steps down, so a
// router's choice never costs more thinking than it asked for.
func ClampThinkingLevel(m Model, level ThinkingLevel) ThinkingLevel {
	if level == "" {
		return ""
	}
	if !m.Reasoning {
		return ThinkingOff
	}
	at := -1
	for i, l := range thinkingLevels {
		if l == level {
			at = i
		}
	}
	if at >= 0 && supportsThinkingLevel(m, level) {
		return level
	}
	if at < 0 {
		// Not a level at all: the lowest the model supports, as pi does.
		for _, l := range thinkingLevels {
			if supportsThinkingLevel(m, l) {
				return l
			}
		}
		return ThinkingOff
	}
	for i := at - 1; i >= 0; i-- {
		if supportsThinkingLevel(m, thinkingLevels[i]) {
			return thinkingLevels[i]
		}
	}
	for i := at + 1; i < len(thinkingLevels); i++ {
		if supportsThinkingLevel(m, thinkingLevels[i]) {
			return thinkingLevels[i]
		}
	}
	return ThinkingOff
}
