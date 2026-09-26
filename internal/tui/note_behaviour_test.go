package tui

import "testing"

// Render snapshots for Phase 3's system notes (interrupted/declined/
// reconnected) and the queued `you` block — see blocks_golden_test.go's
// TestRenderGolden_Note for the generic "note" shape these specialize.

func TestRenderGolden_NoteInterrupted(t *testing.T) {
	withRenderEnv(t, 80)
	assertRenderGolden(t, "note-interrupted", RenderNote("■ Interrupted. Tell kiln what to do instead.", 80))
}

func TestRenderGolden_NoteDeclined(t *testing.T) {
	withRenderEnv(t, 80)
	assertRenderGolden(t, "note-declined", RenderNote(declinedNoteText(PermissionRequest{ToolName: "bash", PrimaryArg: "npm test -- upload"}), 80))
}

func TestRenderGolden_NoteDeclinedEdit(t *testing.T) {
	withRenderEnv(t, 80)
	assertRenderGolden(t, "note-declined-edit", RenderNote(declinedNoteText(PermissionRequest{ToolName: "edit", PrimaryArg: "src/math.js"}), 80))
}

func TestRenderGolden_NoteReconnected(t *testing.T) {
	withRenderEnv(t, 80)
	assertRenderGolden(t, "note-reconnected", RenderNote("↺ Reconnected on attempt 2", 80))
}

func TestRenderGolden_UserQueued(t *testing.T) {
	withRenderEnv(t, 80)
	assertRenderGolden(t, "user-queued", RenderUserMessageMeta("check the other file too", "queued", 80))
}
