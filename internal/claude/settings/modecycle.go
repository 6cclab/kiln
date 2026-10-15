package settings

// NextPermissionMode is the mode one Shift+Tab (or /permissions' "m") moves
// to from cur. It follows Claude Code's cycle: manual -> acceptEdits ->
// plan -> auto -> manual. bypassPermissions and dontAsk are never entered
// by cycling; leaving bypassPermissions goes on to auto, leaving dontAsk
// goes back to manual, as Claude Code does (its "default" is kiln's
// "manual").
func NextPermissionMode(cur PermissionMode) PermissionMode {
	switch cur {
	case ModeManual:
		return ModeAcceptEdits
	case ModeAcceptEdits:
		return ModePlan
	case ModePlan, ModeBypassPermissions:
		return ModeAuto
	default:
		return ModeManual
	}
}
