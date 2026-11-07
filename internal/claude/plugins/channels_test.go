package plugins

import (
	"path/filepath"
	"testing"
)

// A plugin's manifest "channels" resolve to the hub-qualified server name
// (plugin_<plugin>_<server>) MCPServers uses, carrying the display name.
func TestChannels_ResolvesToQualifiedServer(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "design-studio", "0.6.1", map[string]any{
		"mcpServers": map[string]any{
			"design-studio": map[string]any{"type": "http", "url": "https://studio.example/mcp"},
		},
		"channels": []map[string]any{
			{"server": "design-studio", "displayName": "Design studio"},
		},
	})

	got := Channels(Plugin{Key: "design-studio@6cclab", Name: "design-studio", Marketplace: "6cclab", Root: root})
	if len(got) != 1 {
		t.Fatalf("got %d channels, want 1: %+v", len(got), got)
	}
	if got[0].Server != "plugin_design-studio_design-studio" {
		t.Errorf("Server = %q, want plugin_design-studio_design-studio", got[0].Server)
	}
	if got[0].Source != "design-studio" {
		t.Errorf("Source = %q, want design-studio", got[0].Source)
	}
	if got[0].DisplayName != "Design studio" {
		t.Errorf("DisplayName = %q, want Design studio", got[0].DisplayName)
	}
}

// A plugin that declares no channels returns nil, not an error.
func TestChannels_NoneReturnsNil(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "squad", "1.0.0", map[string]any{
		"mcpServers": map[string]any{"squad": map[string]any{"type": "http", "url": "https://x/mcp"}},
	})
	if got := Channels(Plugin{Name: "squad", Root: root}); got != nil {
		t.Errorf("Channels = %+v, want nil", got)
	}
}

// A broken "channels" value is skipped with a warning, not a panic, and
// yields no channels.
func TestChannels_BrokenIsSkipped(t *testing.T) {
	root := t.TempDir()
	// channels as an object instead of an array: unmarshal into []decl fails.
	writeFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"),
		`{"name":"x","version":"1","channels":{"not":"an array"}}`)
	if got := Channels(Plugin{Name: "x", Root: root}); got != nil {
		t.Errorf("Channels = %+v, want nil for broken channels", got)
	}
}
