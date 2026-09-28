package server

import (
	"path/filepath"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	in := map[string]any{"in-host": "192.0.2.10", "chk-agent-auto": true}
	if err := saveSettings(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	want := filepath.Join(dir, "edgekit", "settings.json")
	if got := settingsPath(); got != want {
		t.Fatalf("settings path = %s, want %s", got, want)
	}

	out := loadSettings()
	if out["in-host"] != "192.0.2.10" {
		t.Fatalf("in-host not persisted: %#v", out)
	}
	if out["chk-agent-auto"] != true {
		t.Fatalf("chk-agent-auto not persisted: %#v", out)
	}
}

// TestSettingsMergePreservesKeys checks a client update (e.g. a terminal
// display preference) does not drop keys written by the host itself.
func TestSettingsMergePreservesKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := saveSettings(map[string]any{"kits.disabled": []any{"edgekit.kit.ssh"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := mergeSettings(map[string]any{"term-ts": false, "term-hex": true}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	out := loadSettings()
	if out["term-ts"] != false || out["term-hex"] != true {
		t.Fatalf("client keys not persisted: %#v", out)
	}
	if _, ok := out["kits.disabled"]; !ok {
		t.Fatalf("merge dropped host-managed key: %#v", out)
	}
}
