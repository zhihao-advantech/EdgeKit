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
